#!/usr/bin/env python3
"""Real FUSE/NFS and folder-sync acceptance using the existing multiwriter lab.

Requires functioning kernel mounts; missing prerequisites fail, never skip.
Only lab-owned Redis, directories, helpers and mountpoints are touched.
"""
import argparse
import base64
import errno
import fcntl
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time

from multiwriter_lab import Case, Client, Lab, REPO, digest, manifest, parallel

SCENARIOS = ("native-files", "native-ranges", "native-overlap", "native-append",
             "native-rename", "native-reconnect", "native-checkpoint", "native-crash",
             "native-generation", "native-locks")


class MountedClient(Client):
    def __init__(self, case, name, backend):
        super().__init__(case, name)
        self.backend = backend
        self.env["AFS_NATIVE_HELPER"] = str(case.lab.helper)
        if os.environ.get("AFS_LAB_NATIVE_DEBUG") == "1":
            self.env["AFS_NFS_DEBUG"] = "1"

    def mount(self):
        if self.backend == "sync":
            return super().mount()
        self.mount_count += 1
        logfile = self.base / f"mount-{self.mount_count}.log"
        with logfile.open("w") as log:
            self.process = subprocess.Popen([str(self.case.lab.binary), "--config", str(self.config),
                "mount", self.case.name, str(self.root), "--backend", self.backend, "--foreground"],
                env=self.env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        self.pid = self.process.pid
        def ready():
            if self.process.poll() is not None:
                raise AssertionError(f"{self.name} {self.backend} mount exited: {logfile.read_text()[-6000:]}")
            return "; Ctrl-C flushes and stops." in logfile.read_text()
        self.case.wait(f"{self.name} {self.backend} kernel mount readiness", ready, sample=False)
        status = self.run("status", self.root)[0]
        if status["backend"] != self.backend or status["state"] != "running":
            raise AssertionError(f"native mount status: {status}")
        self.pid = status["pid"]  # helper, rather than its foreground supervisor

    def kill(self):
        if self.backend == "sync":
            return super().kill()
        os.kill(self.pid, signal.SIGKILL)
        self.process.wait(10)
        self.pid = None

    def close(self):
        if self.backend == "sync":
            return super().close()
        self.proxy.partition(False)
        try:
            if self.process:
                try:
                    self.run("unmount", self.root, timeout=30)
                except (AssertionError, subprocess.TimeoutExpired):
                    self.run("unmount", self.root, "--force", timeout=30)
                if self.process.poll() is None:
                    self.process.wait(10)
                self.pid = None
        finally:
            self.proxy.close()


class NativeCase(Case):
    @staticmethod
    def sync_tree(tree):
        # macOS creates AppleDouble sidecars on NFS. They are ordinary durable
        # native entries, while folder sync intentionally ignores ._* names.
        # Other hidden files and every ordinary byte/mode remain in the oracle.
        if sys.platform != "darwin":
            return tree
        return {p: value for p, value in tree.items()
                if not any(part.startswith("._") for part in p.split("/"))}

    def same(self, clients=None):
        clients = clients or self.active
        trees = self._manifests(clients)
        native_index = next((i for i, c in enumerate(clients)
                             if getattr(c, "backend", "sync") != "sync"), None)
        if sys.platform != "darwin" or native_index is None:
            return all(tree == trees[0] for tree in trees[1:])
        full = trees[native_index]
        shared = self.sync_tree(full)
        return all(tree == (full if getattr(c, "backend", "sync") != "sync" else shared)
                   for c, tree in zip(clients, trees))

    @staticmethod
    def _manifests(clients):
        # Keep every complete-tree read, but overlap independent client I/O.
        return parallel(lambda c: manifest(c.root), clients) if clients else []

    def require(self, expected, clients=None):
        self.expectations.update(expected)
        (self.directory / "expected.json").write_text(json.dumps(self.expectations, indent=2))
        def ready():
            trees = self._manifests(clients or self.active)
            return all(all(tree.get(path) == value for path, value in expected.items()) for tree in trees)
        self.wait("expected paths on every client", ready)

    def cold_check(self):
        if sys.platform != "darwin":
            return super().cold_check()
        self.flush()
        self.verify_oracles(self.active)
        full = manifest(self.native[0].root)
        shared = self.sync_tree(full)
        (self.directory / "native-full-tree.json").write_text(json.dumps(full, indent=2))
        (self.directory / "sync-policy-tree.json").write_text(json.dumps(shared, indent=2))
        started = time.monotonic()
        cold = Client(self, "cold-observer")
        cold.mount()
        self.wait("fresh sync client reproduces published tree under sync ignore policy",
                  lambda: manifest(cold.root) == shared)
        # Do not excuse sidecar loss: a separate fresh native mount must read
        # every original native entry, including exact AppleDouble byte hashes.
        native_cold = MountedClient(self, "cold-native-observer", self.native[0].backend)
        native_cold.mount()
        self.wait("fresh native client reproduces all bytes including AppleDouble",
                  lambda: manifest(native_cold.root) == full)
        self.verify_oracles(self.active + [cold, native_cold])
        self.samples.append({"label": "cold sync and native mounts including hydration",
                             "seconds": time.monotonic() - started})
        return len(full)

    def seed(self):
        seed = self.directory / "seed"
        seed.mkdir()
        (seed / "empty-dir").mkdir(mode=0o750)
        (seed / "binary").write_bytes(bytes(range(256)))
        (seed / "executable").write_bytes(b"#!/bin/sh\nexit 0\n")
        (seed / "executable").chmod(0o755)
        (seed / "link").symlink_to("binary")
        self.expectations = manifest(seed)
        self.active[0].run("create", self.name, "--from", seed)

    def execute(self):
        backends = self.lab.args.backends.split(",")
        for index in range(self.lab.args.clients):
            MountedClient(self, f"client-{index}", backends[index % len(backends)])
        self.active = list(self.clients)
        self.native = [c for c in self.active if c.backend != "sync"]
        if len(self.native) < 2:
            raise AssertionError("native concurrency requires at least two native clients")
        self.seed()
        parallel(lambda c: c.mount(), self.active)
        self.require(self.expectations)
        getattr(self, self.name.replace("-", "_"))()
        return self.cold_check()

    def remember_bytes(self, path, data, mode=0o644):
        value = {"type": "file", "size": len(data), "sha256": digest(data), "mode": mode}
        self.require({path: value})
        return value

    def create_shared(self, path, data):
        self.write(self.native[0], path, data)
        self.remember_bytes(path, data)

    def native_files(self):
        def owned_files(item):
            index, client = item
            directory = f"writer-{index}"
            (client.root / directory).mkdir(mode=0o700)
            expected = {directory: {"type": "directory", "mode": 0o700}}
            for number in range(self.lab.args.files):
                path = f"writer-{index}/file-{number}"
                expected[path] = self.write(client, path, self.payload(path, 4096 + number))
            return expected
        for expected in parallel(owned_files, list(enumerate(self.active))):
            self.require(expected)
        for index, client in enumerate(self.native):
            directory = f"writer-{self.active.index(client)}"
            os.chmod(client.root / directory, 0o750)
            self.require({directory: {"type": "directory", "mode": 0o750}})
            path = directory + "/file-0"
            os.truncate(client.root / path, 17)
            self.remember_bytes(path, self.payload(path, 4096)[:17])
            os.chmod(client.root / path, 0o640)
            self.remember_bytes(path, self.payload(path, 4096)[:17], mode=0o640)
            renamed = path + "-renamed"
            os.rename(client.root / path, client.root / renamed)
            self.expectations.pop(path, None)
            self.require({path: None, renamed: {"type": "file", "size": 17,
                "sha256": digest(self.payload(path, 4096)[:17]), "mode": 0o640}})
            destination_dir = f"moved-{index}"
            (client.root / destination_dir).mkdir(mode=0o750)
            destination = destination_dir + "/file"
            os.rename(client.root / renamed, client.root / destination)
            self.require({renamed: None, destination_dir: {"type": "directory", "mode": 0o750},
                destination: {"type": "file", "size": 17,
                "sha256": digest(self.payload(path, 4096)[:17]), "mode": 0o640}})

    def native_ranges(self):
        block = 4096
        expected = bytearray(block * len(self.native))
        self.create_shared("ranges", bytes(expected))
        for round_number in range(self.lab.args.rounds):
            barrier = threading.Barrier(len(self.native))
            def write_range(item):
                index, client = item
                data = self.payload(f"range {round_number}/{index}", block)
                fd = os.open(client.root / "ranges", os.O_RDWR)
                try:
                    barrier.wait(timeout=self.lab.args.timeout)
                    if os.pwrite(fd, data, index * block) != len(data):
                        raise AssertionError("short native range write")
                    os.fsync(fd)
                finally:
                    os.close(fd)
                self.event("write", client=client.name, path="ranges", offset=index * block)
                return index, data
            for index, data in parallel(write_range, list(enumerate(self.native))):
                expected[index * block:(index + 1) * block] = data
            self.remember_bytes("ranges", bytes(expected))

    def native_overlap(self):
        self.create_shared("overlap", bytes(4096))
        for round_number in range(self.lab.args.rounds):
            values = [self.payload(f"overlap {round_number}/{i}", 4096) for i in range(len(self.native))]
            barrier = threading.Barrier(len(self.native))
            def overwrite(item):
                client, data = item
                fd = os.open(client.root / "overlap", os.O_RDWR)
                try:
                    barrier.wait(timeout=self.lab.args.timeout)
                    assert os.pwrite(fd, data, 0) == len(data)
                    os.fsync(fd)
                finally:
                    os.close(fd)
                self.event("write", client=client.name, path="overlap", sha256=digest(data))
            parallel(overwrite, list(zip(self.native, values)))
            winners = []
            def converged_candidate():
                snapshots = [(c.root / "overlap").read_bytes() for c in self.active]
                if snapshots[0] in values and all(data == snapshots[0] for data in snapshots):
                    winners.append(snapshots[0])
                    return True
                return False
            self.wait("one complete submitted overwrite converges on every client", converged_candidate)
            actual = winners[-1]
            self.remember_bytes("overlap", actual)

    def native_append(self):
        # NFSv3 WRITE has no append intent. Its clients may race on offsets;
        # atomic append is a FUSE capability, with NFS/sync as observers here.
        writers = [c for c in self.native if c.backend == "fuse"]
        if len(writers) < 2:
            raise AssertionError("native-append requires two FUSE writers; NFSv3 has no atomic append RPC")
        self.create_shared("append", b"")
        expected = []
        for round_number in range(self.lab.args.rounds):
            barrier = threading.Barrier(len(writers))
            def append(item):
                index, client = item
                data = self.payload(f"append {round_number}/{index}", 128)
                fd = os.open(client.root / "append", os.O_WRONLY | os.O_APPEND)
                try:
                    barrier.wait(timeout=self.lab.args.timeout)
                    assert os.write(fd, data) == len(data)
                    os.fsync(fd)
                finally:
                    os.close(fd)
                self.event("write", client=client.name, path="append", sha256=digest(data))
                return data
            expected.extend(parallel(append, list(enumerate(writers))))
            observations = []
            def all_records():
                snapshots = [(c.root / "append").read_bytes() for c in self.active]
                records = [snapshots[0][i:i + 128] for i in range(0, len(snapshots[0]), 128)]
                if sorted(records) == sorted(expected) and all(data == snapshots[0] for data in snapshots):
                    observations.append(snapshots[0])
                    return True
                return False
            self.wait("every append record exactly once in one common order", all_records)
            actual = observations[-1]
            self.remember_bytes("append", actual)

    def native_rename(self):
        self.create_shared("held", b"before")
        a, b = self.native[:2]
        fd = os.open(a.root / "held", os.O_RDWR)
        try:
            os.rename(b.root / "held", b.root / "renamed")
            assert os.pread(fd, 6, 0) == b"before"
            assert os.pwrite(fd, b"after!", 0) == 6
            os.fsync(fd)
        finally:
            os.close(fd)
        self.expectations.pop("held", None)
        self.require({"held": None})
        self.remember_bytes("renamed", b"after!")
        os.unlink(b.root / "renamed")
        self.require({"renamed": None})
        self.write(a, "renamed", b"after!")
        self.remember_bytes("renamed", b"after!")

    def native_reconnect(self):
        a, b = self.native[:2]
        self.create_shared("cached", b"old cache")
        assert (b.root / "cached").read_bytes() == b"old cache"
        b.proxy.partition(True)
        try:
            self.write(a, "cached", b"new after reconnect")
            self.write(a, "created-offline", b"discovery after reconnect")
        finally:
            b.proxy.partition(False)
        self.remember_bytes("cached", b"new after reconnect")
        self.remember_bytes("created-offline", b"discovery after reconnect")

    def native_checkpoint(self):
        a = self.native[0]
        fd = os.open(a.root / "checkpoint-bytes", os.O_CREAT | os.O_WRONLY, 0o644)
        try:
            assert os.write(fd, b"kernel-buffered checkpoint bytes") == 32
            # No application fsync: the checkpoint's native flush must do it.
            cp = a.run("checkpoint", "create", self.name, "--name", "native-buffered")
        finally:
            os.close(fd)
        shown = a.run("checkpoint", "show", self.name, cp["id"])
        entry = shown["manifest"]["entries"].get("/checkpoint-bytes")
        if not entry or entry.get("size") != 32 or base64.b64decode(entry.get("inline", "")) != b"kernel-buffered checkpoint bytes":
            raise AssertionError(f"checkpoint missed native buffered writes: {entry}")
        for command in (("delete", self.name, "--yes"), ("checkpoint", "restore", self.name, cp["id"], "--yes")):
            try:
                a.run(*command)
            except AssertionError as error:
                if "unmount" not in str(error):
                    raise
            else:
                raise AssertionError(f"mounted workspace mutation accepted: {command}")
        self.remember_bytes("checkpoint-bytes", b"kernel-buffered checkpoint bytes")

    def native_crash(self):
        a, b = self.native[:2]
        self.create_shared("survives-crash", b"published before helper crash")
        a.kill()
        a.run("unmount", a.root, "--force")
        a.process = None
        self.write(b, "peer-after-crash", b"still online")
        a.mount()
        self.remember_bytes("survives-crash", b"published before helper crash")
        self.remember_bytes("peer-after-crash", b"still online")

    def native_generation(self):
        self.create_shared("fenced", b"checkpoint bytes")
        admin = Client(self, "restore-admin")
        cp = admin.run("checkpoint", "create", self.name, "--name", "generation-baseline")
        handles = [(c, os.open(c.root / "fenced", os.O_RDWR)) for c in self.native]
        try:
            admin.run("checkpoint", "restore", self.name, cp["id"], "--yes")
            for client, fd in handles:
                try:
                    os.pwrite(fd, b"stale corruption", 0)
                    os.fsync(fd)
                except OSError as error:
                    if error.errno not in (errno.ESTALE, errno.EIO, errno.ENOENT):
                        raise
                    self.event("stale-write-rejected", client=client.name, errno=error.errno)
                else:
                    raise AssertionError(f"{client.name} stale handle wrote after workspace restore")
        finally:
            for client, fd in handles:
                try:
                    os.close(fd)
                except OSError:
                    pass
        old_clients = list(self.active)
        for client in old_clients:
            client.run("unmount", client.root, "--force")
            if client.process:
                client.process.wait(10)
            client.process, client.pid = None, None
        self.active = []
        for index, previous in enumerate(old_clients):
            client = MountedClient(self, f"fresh-{index}", previous.backend)
            client.mount()
            self.active.append(client)
        self.native = [c for c in self.active if c.backend != "sync"]
        self.remember_bytes("fenced", b"checkpoint bytes")

    def native_locks(self):
        writers = [c for c in self.native if c.backend == "fuse"]
        if len(writers) < 2:
            raise AssertionError("native-locks requires two FUSE writers; NFS locking is disabled")
        self.create_shared("locked", b"advisory lock bytes")
        a = os.open(writers[0].root / "locked", os.O_RDWR)
        b = os.open(writers[1].root / "locked", os.O_RDWR)
        other = os.open(writers[0].root / "locked", os.O_RDWR)
        try:
            fcntl.lockf(a, fcntl.LOCK_EX | fcntl.LOCK_NB)
            try:
                fcntl.lockf(b, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except OSError as error:
                if error.errno not in (errno.EACCES, errno.EAGAIN):
                    raise
            else:
                raise AssertionError("second FUSE mount acquired another owner's conflicting lock")
            # POSIX close releases this process's inode locks even when the
            # descriptor being closed never acquired one. Keep a open so this
            # cannot pass merely because RELEASE discarded the locking handle.
            os.close(other)
            other = None
            fcntl.lockf(b, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fcntl.lockf(b, fcntl.LOCK_UN)
            self.event("cross-mount-lock-conflict-and-second-descriptor-close", clients=[c.name for c in writers[:2]])
        finally:
            if other is not None:
                os.close(other)
            os.close(a)
            os.close(b)

        # A sleeping flock request in the same helper must not block fsync or
        # the final descriptor close that releases the lock it is waiting for.
        holder = os.open(writers[0].root / "locked", os.O_RDWR)
        waiter = None
        ready = self.directory / "flock-waiter-ready"
        try:
            fcntl.flock(holder, fcntl.LOCK_EX | fcntl.LOCK_NB)
            code = """
import errno, fcntl, os, sys
from pathlib import Path
fd = os.open(sys.argv[1], os.O_RDWR)
try:
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError as error:
        if error.errno not in (errno.EACCES, errno.EAGAIN):
            raise
    else:
        raise AssertionError('conflicting flock unexpectedly succeeded')
    Path(sys.argv[2]).write_text('waiting')
    fcntl.flock(fd, fcntl.LOCK_EX)
    print('acquired', flush=True)
finally:
    os.close(fd)
"""
            waiter = subprocess.Popen([sys.executable, "-c", code,
                str(writers[0].root / "locked"), str(ready)],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            def waiting():
                if waiter.poll() is not None:
                    raise AssertionError(f"flock waiter exited before release: {waiter.communicate()}")
                return ready.exists()
            self.wait("same-session flock waiter", waiting, sample=False)
            os.fsync(holder)
            os.close(holder)
            holder = None
            output, error = waiter.communicate(timeout=self.lab.args.timeout)
            if waiter.returncode != 0 or output.strip() != "acquired":
                raise AssertionError(f"flock close did not release waiter: {output!r} {error!r}")
            self.event("same-session-flock-wait-fsync-and-final-close", client=writers[0].name)
        finally:
            if waiter is not None and waiter.poll() is None:
                waiter.kill()
                waiter.communicate(timeout=10)
            if holder is not None:
                os.close(holder)


class NativeLab(Lab):
    case_type = NativeCase

    def __init__(self, args):
        super().__init__(args)
        self.helper = Path(args.helper).resolve() if args.helper else self.output / "afsmount"

    def setup(self):
        super().setup()
        if not self.args.helper:
            subprocess.run(["go", "build", "-o", str(self.helper), "./cmd/afsmount"], cwd=REPO, check=True)
        self.report["helper_sha256"] = digest(self.helper.read_bytes())
        self.report["isolation"] = "real kernel FUSE/NFS mounts and sync processes; one host kernel"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--clients", type=int, default=4)
    parser.add_argument("--backends", default="fuse,nfs,sync", help="comma-separated backends, cycled across clients")
    parser.add_argument("--files", type=int, default=8)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--seed", type=int, default=17)
    parser.add_argument("--timeout", type=float, default=90)
    parser.add_argument("--latency-ms", type=float, default=0)
    parser.add_argument("--output")
    parser.add_argument("--binary")
    parser.add_argument("--helper")
    parser.add_argument("--scenario", choices=SCENARIOS, action="append")
    args = parser.parse_args()
    if args.clients < 2 or min(args.files, args.rounds) < 1 or args.timeout <= 0 or args.latency_ms < 0:
        parser.error("clients >= 2, files/rounds >= 1, timeout > 0, latency-ms >= 0 required")
    backends = args.backends.split(",")
    if any(b not in ("sync", "fuse", "nfs") for b in backends):
        parser.error("backends must be fuse, nfs, or sync")
    if sum(backends[i % len(backends)] != "sync" for i in range(args.clients)) < 2:
        parser.error("at least two native writers are required")
    args.scenario = args.scenario or list(SCENARIOS)
    def interrupt(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupt)
    return NativeLab(args).run()


if __name__ == "__main__":
    sys.exit(main())
