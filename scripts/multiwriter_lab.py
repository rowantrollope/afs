#!/usr/bin/env python3
"""Reproducible, black-box AFS multi-writer lab; Python standard library only.

Owns every process, Redis database and local tree it touches. No external Redis
URL is accepted. Artifacts survive failures; cleanup errors are reported.
"""

import argparse
import asyncio
import concurrent.futures
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import random
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import threading
import time
import traceback


SCENARIOS = ("hydrate", "disjoint", "shared-create", "shared-edit", "chunked", "delete-recreate", "partition",
             "crash", "rename-delete", "symlink")
REPO = Path(__file__).resolve().parents[1]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def signature(path):
    info = path.lstat()
    mode = stat.S_IMODE(info.st_mode)
    if stat.S_ISLNK(info.st_mode):
        return {"type": "symlink", "target": os.readlink(path)}
    if stat.S_ISDIR(info.st_mode):
        return {"type": "directory", "mode": mode}
    if stat.S_ISREG(info.st_mode):
        return {"type": "file", "size": info.st_size,
                "sha256": digest(path.read_bytes()), "mode": mode}
    raise AssertionError(f"unexpected file type: {path}")


def manifest(root):
    result = {}
    # Do not follow symlinks; include dangling links and empty directories.
    for current, dirs, files in os.walk(root):
        dirs[:] = sorted(d for d in dirs if d != ".afs-lite-sync")
        for name in dirs + sorted(files):
            if name.startswith(".afs-sync.tmp.") or ".afssync.tmp." in name:
                continue
            path = Path(current) / name
            result[path.relative_to(root).as_posix()] = signature(path)
    return result


def parallel(function, values):
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(values)) as pool:
        # Materialize all futures; propagate failures after waiting for workers.
        return list(pool.map(function, values))


class Proxy:
    """Dedicated asyncio loop; partition closes existing AND new connections."""

    def __init__(self, upstream, latency_ms, host="127.0.0.1", tls=None):
        self.upstream, self.host, self.tls = upstream, host, tls
        self.delay = latency_ms / 1000
        self.blocked = False
        self.writers = set()
        self.loop = asyncio.new_event_loop()
        self.thread = threading.Thread(target=self.loop.run_forever, daemon=True)
        self.thread.start()
        self.port = self.call(self.start())

    def call(self, coroutine):
        return asyncio.run_coroutine_threadsafe(coroutine, self.loop).result(10)

    async def start(self):
        self.server = await asyncio.start_server(self.accept, "127.0.0.1", 0)
        return self.server.sockets[0].getsockname()[1]

    async def accept(self, reader, writer):
        peer = None
        self.writers.add(writer)
        try:
            if self.blocked:
                return
            remote, peer = await asyncio.open_connection(
                self.host, self.upstream, ssl=self.tls,
                server_hostname=self.host if self.tls else None)
            self.writers.add(peer)
            if self.blocked:
                return

            async def copy(source, destination):
                while not self.blocked:
                    data = await source.read(65536)
                    if not data:
                        break
                    if self.delay:
                        await asyncio.sleep(self.delay)
                    destination.write(data)
                    await destination.drain()

            tasks = [asyncio.create_task(copy(reader, peer)),
                     asyncio.create_task(copy(remote, writer))]
            try:
                await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
            finally:
                for task in tasks:
                    task.cancel()
                await asyncio.gather(*tasks, return_exceptions=True)
        except (OSError, asyncio.CancelledError):
            pass
        finally:
            for connection in (writer, peer):
                if connection:
                    self.writers.discard(connection)
                    connection.close()
                    try:
                        await connection.wait_closed()
                    except OSError:
                        pass

    async def switch(self, blocked):
        self.blocked = blocked
        if blocked:
            for writer in list(self.writers):
                writer.close()

    def partition(self, blocked):
        self.call(self.switch(blocked))

    def close(self):
        async def stop():
            await self.switch(True)
            self.server.close()
            await self.server.wait_closed()
            tasks = [t for t in asyncio.all_tasks() if t is not asyncio.current_task()]
            for task in tasks:
                task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
        self.call(stop())
        self.loop.call_soon_threadsafe(self.loop.stop)
        self.thread.join(5)
        self.loop.close()


class Client:
    def __init__(self, case, name):
        self.case, self.name = case, name
        self.base = case.directory / name
        self.root = self.base / "workspace"
        self.base.mkdir()
        self.proxy = Proxy(case.lab.port, case.lab.args.latency_ms)
        self.pid = None
        self.process = None
        self.mount_count = 0
        self.env = {k: v for k, v in os.environ.items()
                    if not k.startswith("AFS_") and k not in ("HOME", "XDG_CONFIG_HOME")}
        # Also avoid accidentally configuring this run through a user's home.
        self.env.update(HOME=str(self.base / "home"),
                        XDG_CONFIG_HOME=str(self.base / "config"),
                        AFS_STATE_DIR=str(self.base / "state"))
        self.config = self.base / "config.json"
        self.config.write_text(json.dumps({"redis": f"redis://127.0.0.1:{self.proxy.port}/0"}))
        case.clients.append(self)

    def run(self, *args, timeout=None):
        started = time.monotonic()
        proc = subprocess.run([str(self.case.lab.binary), "--config", str(self.config),
                               "--json", *map(str, args)], env=self.env,
                              capture_output=True, text=True,
                              timeout=timeout or self.case.lab.args.timeout)
        with (self.base / "cli.jsonl").open("a") as log:
            log.write(json.dumps({"args": list(map(str, args)), "code": proc.returncode,
                                  "seconds": time.monotonic() - started,
                                  "stdout": proc.stdout, "stderr": proc.stderr}) + "\n")
        if proc.returncode:
            raise AssertionError(f"{self.name} afs {' '.join(map(str, args))}: {proc.stderr.strip()}")
        return json.loads(proc.stdout) if proc.stdout.strip() else None

    def mount(self):
        # Foreground supervision gives us the process handle before startup;
        # failed/slow hydration cannot orphan a detached daemon.
        self.mount_count += 1
        logfile = self.base / f"mount-{self.mount_count}.log"
        with logfile.open("w") as log:
            self.process = subprocess.Popen([str(self.case.lab.binary), "--config", str(self.config),
                "mount", self.case.name, str(self.root), "--foreground"], env=self.env,
                stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        self.pid = self.process.pid
        def ready():
            if self.process.poll() is not None:
                raise AssertionError(f"{self.name} mount exited: {logfile.read_text()[-4000:]}")
            return "; Ctrl-C flushes and stops." in logfile.read_text()
        self.case.wait(f"{self.name} mount readiness", ready, sample=False)

    def stop(self):
        os.kill(self.pid, signal.SIGSTOP)
        # Observe the stop, so no daemon can run between the write barriers.
        self.case.wait("daemon stopped", lambda: "T" in subprocess.check_output(
            ["ps", "-o", "stat=", "-p", str(self.pid)], text=True), sample=False)

    def resume(self):
        os.kill(self.pid, signal.SIGCONT)

    def kill(self):
        self.process.kill()
        self.process.wait(5)
        self.pid = None

    def close(self):
        try:
            self.proxy.partition(False)
            if self.process and self.process.poll() is None:
                try:
                    self.resume()
                    self.run("unmount", self.root, "--force", timeout=10)
                except (ProcessLookupError, AssertionError, subprocess.TimeoutExpired):
                    pass  # The owned process handle is the final cleanup fallback.
        finally:
            try:
                if self.process and self.process.poll() is None:
                    self.process.kill()
                    self.process.wait(5)
                self.pid = None
            finally:
                self.proxy.close()


class Case:
    def __init__(self, lab, name):
        self.lab, self.name = lab, name
        self.directory = lab.output / name
        self.directory.mkdir()
        self.clients = []
        self.samples = []
        self.writes = 0
        self.sequence = 0
        self.expectations = {}
        self.candidate_expectations = {}
        self.event_lock = threading.Lock()

    def wait(self, label, predicate, sample=True):
        start = time.monotonic()
        last = None
        while time.monotonic() - start < self.lab.args.timeout:
            try:
                if predicate():
                    if sample:
                        self.samples.append({"label": label, "seconds": time.monotonic() - start})
                    return
            except (FileNotFoundError, OSError) as error:
                last = str(error)
            time.sleep(0.05)
        raise AssertionError(f"timeout: {label}" + (f" ({last})" if last else ""))

    def event(self, kind, **fields):
        with self.event_lock:
            with (self.directory / "operations.jsonl").open("a") as log:
                log.write(json.dumps({"time": time.time(), "kind": kind, **fields}) + "\n")
            if kind == "write":
                self.writes += 1

    def write(self, client, path, data=None, target=None):
        full = client.root / path
        full.parent.mkdir(parents=True, exist_ok=True)
        if target is None:
            # Each write is one application save via atomic rename. This is
            # a process-crash workload, not a power-loss durability test.
            temp = full.parent / (".afs-sync.tmp.lab-" + client.name)
            with temp.open("wb") as stream:
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
            temp.chmod(0o644)
            temp.replace(full)
        else:
            temp = full.parent / (".afs-sync.tmp.lab-" + client.name)
            temp.symlink_to(target)
            temp.replace(full)
        # Compute the oracle from intended bytes, never from a live path that
        # a competing daemon may already have overwritten.
        expected = ({"type": "symlink", "target": target} if target is not None else
                    {"type": "file", "size": len(data), "sha256": digest(data), "mode": 0o644})
        self.event("write", client=client.name, path=path, expected=expected)
        return expected

    def payload(self, label, size=256):
        prefix = f"seed={self.lab.args.seed} case={self.name} {label}\n".encode()
        return (prefix * (size // len(prefix) + 1))[:size]

    def same(self, clients=None):
        snapshots = [manifest(c.root) for c in (clients or self.active)]
        return all(m == snapshots[0] for m in snapshots[1:])

    def require(self, expected, clients=None):
        self.expectations.update(expected)
        (self.directory / "expected.json").write_text(json.dumps(self.expectations, indent=2))
        def ready():
            trees = [manifest(c.root) for c in (clients or self.active)]
            return all(all(tree.get(path) == value for path, value in expected.items()) for tree in trees)
        self.wait("expected paths on every client", ready)

    def candidates(self, path, expected, clients=None, alternatives=()):
        self.expectations.pop(path, None)
        self.candidate_expectations[path] = {"expected": expected, "alternatives": alternatives}
        (self.directory / ("candidates-" + str(self.sequence) + ".json")).write_text(
            json.dumps({"path": path, "alternatives": alternatives, "expected": expected}, indent=2))
        self.sequence += 1
        def preserved():
            for client in (clients or self.active):
                tree = manifest(client.root)
                values = [v for p, v in tree.items() if any(
                    p == candidate or p.startswith(candidate + ".conflict-")
                    for candidate in (path, *alternatives))]
                if not all(value in values for value in expected):
                    return False
            return True
        self.wait(f"all {len(expected)} candidate versions preserved on every client: {path}", preserved)

    def frozen_writes(self, path, values, targets=False):
        parallel(lambda c: c.stop(), self.active)
        try:
            return parallel(lambda item: self.write(item[0], path,
                target=item[1] if targets else None, data=None if targets else item[1]),
                list(zip(self.active, values)))
        finally:
            parallel(lambda c: c.resume(), self.active)

    def flush(self):
        def save(client):
            client.run("cp", "create", self.name,
                       "--name", f"lab-{client.name}-{self.sequence}")
        # Checkpoint creation takes a workspace-wide lease. Serialize receipts;
        # simultaneous checkpoint contention is unrelated to writer convergence.
        for client in self.active:
            save(client)
        self.sequence += 1
        self.wait("complete trees converge after successful flushes", self.same)

    def cold_check(self):
        self.flush()
        self.verify_oracles(self.active)
        expected = manifest(self.active[0].root)
        cold = Client(self, "cold-observer")
        started = time.monotonic()
        cold.mount()
        self.wait("fresh client reproduces published tree", lambda: manifest(cold.root) == expected)
        self.verify_oracles(self.active + [cold])
        self.samples.append({"label": "cold mount including hydration", "seconds": time.monotonic() - started})
        return len(expected)

    def verify_oracles(self, clients):
        # Preserve an independent workload oracle through flush and hydration;
        # unanimous disappearance must never count as successful convergence.
        expected = dict(self.expectations)
        candidates = dict(self.candidate_expectations)
        self.require(expected, clients)
        for path, oracle in candidates.items():
            self.candidates(path, oracle["expected"], clients, oracle["alternatives"])
        if self.name == "disjoint":
            wanted = {p for p, value in expected.items() if value is not None}
            for client in clients:
                actual = {p for p, value in manifest(client.root).items() if value["type"] != "directory"}
                if actual != wanted:
                    raise AssertionError(f"{client.name}: unexpected disjoint files: {sorted(actual - wanted)}")

    def disjoint(self):
        rng = random.Random(self.lab.args.seed)
        expected = {}
        owned = [[] for _ in self.active]
        for round_number in range(self.lab.args.rounds):
            plans = []
            for index, client in enumerate(self.active):
                jobs = []
                for number in range(self.lab.args.files):
                    path = f"agent-{index}/file-{round_number}-{number}.bin"
                    jobs.append((path, self.payload(path, rng.choice([0, 97, 4096, 32768]))))
                    owned[index].append(path)
                plans.append((client, jobs))
            def work(plan):
                client, jobs = plan
                return {path: self.write(client, path, data) for path, data in jobs}
            start = time.monotonic()
            for changes in parallel(work, plans):
                expected.update(changes)
            self.require(expected)
            elapsed = time.monotonic() - start
            count = len(self.active) * self.lab.args.files
            byte_count = sum(len(data) for _, jobs in plans for _, data in jobs)
            self.samples.append({"label": "disjoint batch end-to-end", "seconds": elapsed,
                                 "writes": count, "bytes": byte_count,
                                 "files_per_second": count / elapsed,
                                 "bytes_per_second": byte_count / elapsed})
            # Mutations are on paths owned by one writer, so there is an exact oracle.
            def mutate(item):
                index, client = item
                changes = {}
                for number, path in enumerate(owned[index][-self.lab.args.files:]):
                    action = number % 3
                    if action == 0:
                        (client.root / path).unlink()
                        changes[path] = None
                        self.event("delete", client=client.name, path=path)
                    elif action == 1:
                        destination = path + ".renamed"
                        (client.root / path).rename(client.root / destination)
                        changes[path] = None
                        changes[destination] = expected[path]
                        self.event("rename", client=client.name, path=path, destination=destination)
                    else:
                        changes[path] = self.write(client, path, self.payload(f"edit:{path}"))
                return changes
            for changes in parallel(mutate, list(enumerate(self.active))):
                expected.update(changes)
            self.require(expected)

    def hydrate(self):
        # All clients have just loaded the same imported project concurrently.
        self.require(self.expectations)
        self.wait("all hydrated trees match", self.same)
        for round_number in range(self.lab.args.rounds):
            def edit(item):
                index, client = item
                return {f"project/agent-{index}/file-{number}.txt": self.write(client,
                    f"project/agent-{index}/file-{number}.txt",
                    self.payload(f"hydrated edit round={round_number} agent={index} file={number}"))
                    for number in range(self.lab.args.files)}
            expected = {}
            for changes in parallel(edit, list(enumerate(self.active))):
                expected.update(changes)
            self.require(expected)

    def shared(self, existing=False, chunked=False):
        for round_number in range(self.lab.args.rounds):
            path = f"shared-{round_number}.bin"
            size = 1024 * 1024 + 317 if chunked else 256
            if existing:
                baseline = self.write(self.active[0], path, self.payload("baseline", size))
                self.require({path: baseline})
                self.flush()
            values = [self.payload(f"round={round_number} writer={i}", size)
                      for i in range(len(self.active))]
            expected = self.frozen_writes(path, values)
            self.candidates(path, expected)

    def partition(self):
        offline, peer = self.active[:2]
        path = "partition.txt"
        initial = self.write(peer, path, self.payload("baseline"))
        self.require({path: initial})
        self.flush()
        offline.proxy.partition(True)
        self.event("partition", client=offline.name, blocked=True)
        try:
            local = self.write(offline, path, self.payload("offline edit"))
            remote = self.write(peer, path, self.payload("connected edit"))
            independent = self.write(offline, "offline-only.txt", self.payload("offline new file"))
            self.require({path: remote}, self.active[1:])
            self.wait("offline client reports disconnected", lambda:
                      offline.run("status", offline.root)[0].get("sync", {}).get("connected") is False)
        finally:
            offline.proxy.partition(False)
            self.event("partition", client=offline.name, blocked=False)
        self.candidates(path, [local, remote])
        self.require({"offline-only.txt": independent})

    def delete_recreate(self):
        writer = self.active[0]
        data = self.payload("identical content across remote recreation")
        expected = {"nested/recreated.txt": self.write(writer, "nested/recreated.txt", data),
                    "nested/pointer": self.write(writer, "nested/pointer", target="same-target")}
        self.require(expected)
        self.flush()
        for path in expected:
            (writer.root / path).unlink()
            self.event("delete-before-identical-recreate", client=writer.name, path=path)
        self.require(dict.fromkeys(expected))
        # A new microVM has no old deletion baseline. It republishes precisely
        # the same content, which existing peers must accept as a new creation.
        newcomer = Client(self, "recreator")
        newcomer.mount()
        self.active.append(newcomer)
        self.write(newcomer, "nested/recreated.txt", data)
        self.write(newcomer, "nested/pointer", target="same-target")
        self.require(expected)

    def crash(self):
        crashed, peer = self.active[:2]
        initial = self.write(peer, "crash.txt", self.payload("baseline"))
        self.require({"crash.txt": initial})
        self.flush()
        crashed.stop()
        local = self.write(crashed, "crash.txt", self.payload("unuploaded at SIGKILL"))
        extra = self.write(crashed, "crash-only.txt", self.payload("new at SIGKILL"))
        crashed.kill()
        self.event("SIGKILL", client=crashed.name)
        remote = self.write(peer, "crash.txt", self.payload("peer while dead"))
        self.require({"crash.txt": remote}, self.active[1:])
        # The established local directory and persisted baseline survive the guest.
        crashed.mount()
        self.event("warm-remount", client=crashed.name)
        self.candidates("crash.txt", [local, remote])
        self.require({"crash-only.txt": extra})

    def rename_delete(self):
        a, b = self.active[:2]
        initial = self.write(a, "delete.txt", self.payload("baseline"))
        source = self.write(a, "source.txt", self.payload("rename source"))
        self.require({"delete.txt": initial, "source.txt": source})
        self.flush()
        b.stop()
        try:
            edited = self.write(b, "delete.txt", self.payload("edit versus delete"))
            destination = self.write(b, "destination.txt", self.payload("competing destination"))
            (a.root / "delete.txt").unlink()
            (a.root / "source.txt").rename(a.root / "destination.txt")
            self.event("delete-and-rename", client=a.name)
            self.require({"delete.txt": None, "source.txt": None, "destination.txt": source},
                         [c for c in self.active if c is not b])
        finally:
            b.resume()
        self.candidates("delete.txt", [edited])
        self.candidates("destination.txt", [source, destination])
        initial = self.write(a, "stale-source.txt", self.payload("stale rename source"))
        self.require({"stale-source.txt": initial})
        self.flush()
        b.stop()
        try:
            (b.root / "stale-source.txt").rename(b.root / "stale-renamed.txt")
            self.event("rename-stale-source", client=b.name)
            changed = self.write(a, "stale-source.txt", self.payload("peer changed source"))
            self.require({"stale-source.txt": changed}, [c for c in self.active if c is not b])
        finally:
            b.resume()
        self.candidates("stale-source.txt", [initial, changed], alternatives=("stale-renamed.txt",))

    def symlink(self):
        # Warm-recovery retarget collision gives a deterministic stale baseline.
        a, b = self.active[:2]
        initial = self.write(a, "pointer", target="baseline-target")
        self.require({"pointer": initial})
        self.flush()
        b.stop()
        local = self.write(b, "pointer", target="offline-target")
        b.kill()
        remote = self.write(a, "pointer", target="published-target")
        self.require({"pointer": remote}, [c for c in self.active if c is not b])
        b.mount()
        self.candidates("pointer", [local, remote])
        for round_number in range(self.lab.args.rounds):
            path = f"concurrent-link-{round_number}"
            targets = [f"target-{round_number}-{i}" for i in range(len(self.active))]
            expected = self.frozen_writes(path, targets, targets=True)
            self.candidates(path, expected)

    def execute(self):
        for index in range(self.lab.args.clients):
            Client(self, f"client-{index}")
        self.active = list(self.clients)
        if self.name == "hydrate":
            seed = self.directory / "seed-project"
            for index in range(self.lab.args.clients):
                directory = seed / "project" / f"agent-{index}"
                directory.mkdir(parents=True)
                for number in range(self.lab.args.files):
                    (directory / f"file-{number}.txt").write_bytes(self.payload(f"seed {index}/{number}", 4096))
            (seed / "empty-directory").mkdir(mode=0o750)
            (seed / "run.sh").write_bytes(b"#!/bin/sh\nprintf 'agent workspace\\n'\n")
            (seed / "run.sh").chmod(0o755)
            (seed / "current-project").symlink_to("project")
            self.expectations = manifest(seed)
            self.active[0].run("create", self.name, "--from", seed)
        else:
            self.active[0].run("create", self.name)
        started = time.monotonic()
        parallel(lambda c: c.mount(), self.active)
        self.samples.append({"label": "all initial mounts", "seconds": time.monotonic() - started})
        if self.name in ("shared-create", "shared-edit", "chunked"):
            self.shared(existing=self.name != "shared-create", chunked=self.name == "chunked")
        else:
            getattr(self, self.name.replace("-", "_"))()
        return self.cold_check()

    def capture(self):
        errors = []
        for client in self.clients:
            try:
                tree = manifest(client.root)
                (client.base / "manifest.json").write_text(json.dumps(tree, indent=2))
                differences = {path: {"expected": value, "actual": tree.get(path)}
                               for path, value in self.expectations.items() if tree.get(path) != value}
                for path, oracle in self.candidate_expectations.items():
                    values = [value for p, value in tree.items() if any(
                        p == candidate or p.startswith(candidate + ".conflict-")
                        for candidate in (path, *oracle["alternatives"]))]
                    missing = [value for value in oracle["expected"] if value not in values]
                    if missing:
                        differences[path] = {"missing_candidates": missing, "actual_candidates": values}
                (client.base / "differences.json").write_text(json.dumps(differences, indent=2))
                if client.pid:
                    (client.base / "status.json").write_text(json.dumps(client.run("status", client.root, timeout=5), indent=2))
            except Exception as error:
                errors.append(f"{client.name}: {error}")
        return errors

    def close(self):
        errors = []
        for client in reversed(self.clients):
            try:
                client.close()
            except Exception as error:
                errors.append(f"{client.name}: {error}")
        return errors


class Lab:
    case_type = Case
    def __init__(self, args):
        self.args = args
        if args.output:
            self.output = Path(args.output).resolve()
            self.output.mkdir(parents=True, exist_ok=False)
        else:
            self.output = Path(tempfile.mkdtemp(prefix="afs-multiwriter-")).resolve()
        self.redis = None
        self.report = {"schema_version": 1, "configuration": vars(args), "platform": platform.platform(),
                       "python": sys.version, "isolation": "independent processes; one host kernel",
                       "output": str(self.output), "cases": [], "cleanup_errors": []}
        self.binary = Path(args.binary).resolve() if args.binary else self.output / "afs"

    def setup(self):
        for name in ("redis-server", "redis-cli", "ps"):
            if not shutil.which(name):
                raise RuntimeError(f"required executable missing: {name}")
        if not self.args.binary:
            subprocess.run(["go", "build", "-o", str(self.binary), "./cmd/afs"], cwd=REPO, check=True)
        self.report["binary_sha256"] = digest(self.binary.read_bytes())
        self.report["git_revision"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=REPO, text=True).strip() if (REPO / ".git").exists() else "unavailable"
        self.report["git_dirty"] = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=REPO)) if (REPO / ".git").exists() else None
        self.report["redis_version"] = subprocess.check_output(["redis-server", "--version"], text=True).strip()
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.port = listener.getsockname()[1]
        redis_dir = self.output / "redis"
        redis_dir.mkdir()
        with (redis_dir / "server.log").open("w") as log:
            self.redis = subprocess.Popen(["redis-server", "--bind", "127.0.0.1", "--port", str(self.port),
                "--save", "", "--appendonly", "yes", "--appendfsync", "always", "--dir", str(redis_dir)],
                stdout=log, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if self.redis.poll() is not None:
                raise RuntimeError("disposable Redis exited; inspect redis/server.log")
            try:
                with socket.create_connection(("127.0.0.1", self.port), timeout=0.2) as connection:
                    connection.sendall(b"*1\r\n$4\r\nPING\r\n")
                    if connection.recv(32) == b"+PONG\r\n":
                        # The ephemeral port reservation was released before
                        # Redis bound it. Prove this server is our child before
                        # any workspace mutation, even if another process raced us.
                        info = subprocess.check_output(["redis-cli", "-h", "127.0.0.1", "-p", str(self.port),
                            "INFO", "server"], text=True, timeout=2,
                            env={k: v for k, v in os.environ.items() if not k.startswith("REDISCLI_")})
                        if f"process_id:{self.redis.pid}" not in info.splitlines():
                            raise RuntimeError("ephemeral port belongs to another server; refusing to use it")
                        self.report["redis_pid"] = self.redis.pid
                        return
            except OSError:
                pass
            time.sleep(0.05)
        raise RuntimeError("disposable Redis readiness timeout")

    def save(self):
        (self.output / "report.json").write_text(json.dumps(self.report, indent=2))
        rows = ["# AFS multi-writer lab", "", f"Artifacts: `{self.output}`", "",
                "| Scenario | Result | Seconds | Writes |", "|---|---|---:|---:|"]
        for case in self.report["cases"]:
            rows.append(f"| {case['name']} | {case['status']} | {case['seconds']:.2f} | {case['writes']} |")
        for case in self.report["cases"]:
            if case.get("error"):
                rows.extend(["", f"**{case['name']}:** {case['error']}", ""])
        rows += ["", "Timings include Python orchestration and local Redis with AOF fsync always.",
                 "These are process-lab measurements, not microVM or production benchmarks.",
                 "See report.json for labeled samples, configuration, failures and cleanup status."]
        (self.output / "report.md").write_text("\n".join(rows) + "\n")

    def run(self):
        print(f"Artifacts: {self.output}", flush=True)
        try:
            self.setup()
            for name in self.args.scenario or SCENARIOS:
                case = self.case_type(self, name)
                started = time.monotonic()
                result = {"name": name, "status": "pass"}
                print(f"RUN  {name} ({self.args.clients} writers)", flush=True)
                try:
                    result["tree_entries"] = case.execute()
                except (KeyboardInterrupt, SystemExit):
                    result.update(status="interrupted", error="run interrupted before verification completed")
                    raise
                except Exception as error:
                    result.update(status="fail", error=str(error), traceback=traceback.format_exc())
                finally:
                    result["capture_errors"] = case.capture()
                    try:
                        info = subprocess.check_output(["redis-cli", "-h", "127.0.0.1", "-p", str(self.port),
                                                        "INFO"], text=True, timeout=5,
                            env={k: v for k, v in os.environ.items() if not k.startswith("REDISCLI_")})
                        (case.directory / "redis-info.txt").write_text(info)
                    except Exception as error:
                        result["capture_errors"].append(f"Redis INFO: {error}")
                    cleanup_errors = case.close()
                    if cleanup_errors:
                        result.update(status="fail", cleanup_errors=cleanup_errors)
                    result.update(seconds=time.monotonic() - started, writes=case.writes, samples=case.samples)
                    batches = [s["seconds"] for s in case.samples if s["label"] == "disjoint batch end-to-end"]
                    if batches:
                        batches.sort()
                        result["disjoint_batch_seconds"] = {"p50": batches[math.ceil(.5 * len(batches)) - 1],
                            "p95": batches[math.ceil(.95 * len(batches)) - 1], "max": max(batches), "count": len(batches)}
                    self.report["cases"].append(result)
                    self.save()
                print(f"{result['status'].upper():4} {name}: {result['seconds']:.2f}s" +
                      (f" — {result['error']}" if result.get("error") else ""), flush=True)
        except (KeyboardInterrupt, SystemExit):
            self.report["interrupted"] = True
            print("Interrupted; stopping owned processes and preserving evidence.", file=sys.stderr)
        except Exception as error:
            self.report["setup_error"] = str(error)
            self.report["setup_traceback"] = traceback.format_exc()
            print(f"ERROR: {error}", file=sys.stderr)
        finally:
            if self.redis:
                self.redis.terminate()
                try:
                    self.redis.wait(10)
                except subprocess.TimeoutExpired:
                    self.redis.kill()
                    self.redis.wait(5)
            self.save()
        return 1 if (self.report.get("setup_error") or self.report.get("interrupted") or self.report["cleanup_errors"] or
                     any(c["status"] != "pass" for c in self.report["cases"])) else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--clients", type=int, default=4)
    parser.add_argument("--files", type=int, default=20, help="files per writer per disjoint round")
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--seed", type=int, default=1)
    parser.add_argument("--timeout", type=float, default=45, help="per command/assertion seconds")
    parser.add_argument("--latency-ms", type=float, default=0, help="delay per proxy read, each direction; not precise RTT")
    parser.add_argument("--output", help="new artifact directory; default: retained temporary directory")
    parser.add_argument("--binary", help="existing binary to test; default: build this checkout")
    parser.add_argument("--scenario", choices=SCENARIOS, action="append")
    args = parser.parse_args()
    if args.clients < 2 or min(args.files, args.rounds) < 1 or args.timeout <= 0 or args.latency_ms < 0:
        parser.error("clients >= 2, files/rounds >= 1, timeout > 0, latency-ms >= 0 required")
    if args.scenario and len(set(args.scenario)) != len(args.scenario):
        parser.error("do not repeat the same scenario")
    # SIGTERM from supervisors follows the same cleanup path as Ctrl-C.
    def interrupt(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupt)
    return Lab(args).run()


if __name__ == "__main__":
    sys.exit(main())
