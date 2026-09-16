#!/usr/bin/env python3
"""Two-host folder-sync acceptance. Standard library; see docs/two-system-sync.md.

Set AFS_TEST_REDIS on both machines. Run `host` on A, forward its control port
with SSH, then run `join` on B. Coordination never uses the synchronized tree.
Only newly created test workspaces are modified on your existing Redis server.
The adjacent multiwriter_lab.py supplies retained process and proxy primitives.
"""

import argparse
import hashlib
import hmac
import math
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import platform
import secrets
import shutil
import signal
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import traceback
import urllib.error
import urllib.request
import urllib.parse
import uuid

from multiwriter_lab import Client, Proxy, digest, signature

SCENARIOS = ("basic", "mutations", "bulk", "large", "ignore", "shared-create",
             "shared-edit", "symlink-conflict", "delete-edit", "rename-edit",
             "partition", "crash")
PROTOCOL = 1


def save_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, ensure_ascii=True) + "\n")
    temporary.replace(path)


def source_hash():
    return digest(Path(__file__).read_bytes() +
                  Path(__file__).with_name("multiwriter_lab.py").read_bytes())


def tree(root):
    """Strict portable manifest, including ignored fixtures for separate validation."""
    if not root.is_dir():
        raise AssertionError("sync root is missing")
    result = {}
    def fail(error):
        raise error
    for current, dirs, files in os.walk(root, followlinks=False, onerror=fail):
        # Only private daemon state/staging is invisible to the oracle.
        dirs[:] = sorted(d for d in dirs if d != ".afs-lite-sync")
        for name in dirs + sorted(files):
            if name.startswith(".afs-sync.tmp.") or ".afssync.tmp." in name:
                continue
            path = Path(current) / name
            relative = path.relative_to(root).as_posix()
            # macOS and Linux represent Unicode filenames differently.
            # Keep raw names in artifacts; fixtures use normalization-stable characters.
            result[relative] = signature(path)
    return result


def differences(actual, expected, candidates):
    """No peer-derived oracle: even unanimous corruption/disappearance must fail."""
    problems = []
    for path, value in expected.items():
        if actual.get(path) != value:
            problems.append({"path": path, "expected": value, "actual": actual.get(path)})
    claimed = set(expected)
    for rule in candidates:
        names = rule["paths"]
        values = {p: v for p, v in actual.items()
                  if any(p == name or p.startswith(name + ".conflict-") for name in names)}
        claimed.update(values)
        if rule.get("canonical", True) and names[0] not in values:
            problems.append({"missing_canonical": names[0]})
        for value in rule["values"]:
            if value not in values.values():
                problems.append({"missing_version": names, "expected": value})
        for path, value in values.items():
            if value not in rule["values"]:
                problems.append({"unexpected_version": path, "actual": value})
    for path in sorted(set(actual) - claimed):
        problems.append({"unexpected_path": path, "actual": actual[path]})
    return problems


class Coordination:
    """One authenticated session per role; no filesystem/command RPC endpoints."""
    def __init__(self, token, settings):
        self.token, self.settings = token, settings
        self.lock = threading.Lock()
        self.sessions, self.steps = {}, {}
        self.aborted = None
        self.case_errors = {}
        self.join_departed = threading.Event()

    def request(self, message):
        with self.lock:
            role, session = message.get("role"), message.get("session")
            if role not in ("a", "b") or not isinstance(session, str):
                raise ValueError("invalid role/session")
            if self.aborted:
                raise RuntimeError(self.aborted)
            if message.get("op") == "hello":
                previous = self.sessions.get(role)
                if previous and previous != session:
                    raise ValueError(f"role {role} is already occupied; restart both runners")
                if message.get("source") != source_hash():
                    raise ValueError("runner sources differ; use the same scripts on both systems")
                self.sessions[role] = session
                return self.settings
            if self.sessions.get(role) != session:
                raise ValueError("unregistered session")
            if message.get("op") == "abort":
                self.aborted = f"system {role.upper()} failed: {message.get('error', 'unknown error')}"
                return {}
            if message.get("op") == "goodbye" and role == "b":
                return {}
            scope = message.get("scope", "run")
            if not isinstance(scope, str):
                raise ValueError("invalid scope")
            if message.get("op") == "abort-case":
                self.case_errors.setdefault(scope, f"system {role.upper()}: {message.get('error')}")
                return {}
            if message.get("op") != "exchange":
                raise ValueError("unknown operation")
            if scope in self.case_errors:
                raise RuntimeError(self.case_errors[scope])
            step = message.get("step")
            if not isinstance(step, int) or step < 0:
                raise ValueError("invalid barrier step")
            entry = self.steps.setdefault((scope, step), {"label": message.get("label"), "values": {}})
            if entry["label"] != message.get("label"):
                error = f"barrier mismatch at {step}: {entry['label']} / {message.get('label')}"
                self.case_errors[scope] = error
                raise RuntimeError(error)
            data = message.get("data")
            if role in entry["values"] and entry["values"][role] != data:
                raise ValueError("barrier retry changed its payload")
            entry["values"][role] = data
            # Keep the immediately previous step for retries after a lost response.
            for old in list(self.steps):
                if old[0] == scope and old[1] < step - 1 and len(self.steps[old]["values"]) == 2:
                    del self.steps[old]
            return {"ready": len(entry["values"]) == 2, "values": entry["values"]}


def serve_control(state, port):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            self.connection.settimeout(5)
            code = 200
            try:
                auth = self.headers.get("Authorization", "")
                if not hmac.compare_digest(auth, "Bearer " + state.token):
                    raise ValueError("authentication failed")
                if self.path != "/control":
                    raise ValueError("unknown endpoint")
                size = int(self.headers.get("Content-Length", "0"))
                if not 0 < size <= 32 * 1024 * 1024:
                    raise ValueError("invalid request size")
                message = json.loads(self.rfile.read(size))
                result = state.request(message)
            except (ValueError, RuntimeError, OSError) as error:
                code, result = 400, {"error": str(error)}
            body = json.dumps(result).encode()
            try:
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                self.wfile.flush()
                if code == 200 and message.get("op") == "goodbye":
                    state.join_departed.set()
            except OSError:
                pass
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


class Peer:
    def __init__(self, role, port, token, timeout):
        self.role, self.token, self.timeout = role, token, timeout
        self.url = f"http://127.0.0.1:{port}/control"
        self.session, self.step = uuid.uuid4().hex, 0
        self.scope = "run"
        # Never send the control credential through an environment HTTP proxy.
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def call(self, op, **fields):
        body = json.dumps(dict(op=op, role=self.role, session=self.session, scope=self.scope, **fields)).encode()
        request = urllib.request.Request(self.url, data=body,
                    headers={"Authorization": "Bearer " + self.token,
                             "Content-Type": "application/json"})
        try:
            with self.opener.open(request, timeout=min(5, self.timeout)) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            try:
                detail = json.loads(error.read()).get("error", str(error))
            finally:
                error.close()
            raise RuntimeError(detail) from error

    def exchange(self, label, data=None, timeout=None):
        deadline = time.monotonic() + (timeout or self.timeout)
        last = "peer has not reached this barrier"
        while time.monotonic() < deadline:
            try:
                reply = self.call("exchange", step=self.step, label=label, data=data)
                if reply["ready"]:
                    self.step += 1
                    return reply["values"]
            except (urllib.error.URLError, TimeoutError, OSError) as error:
                last = str(error)
            time.sleep(.1)
        raise TimeoutError(f"{label}: coordination timeout: {last}")


def redis_endpoint(url):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme not in ("redis", "rediss") or not parsed.hostname:
        raise ValueError("AFS_TEST_REDIS must be a redis:// or rediss:// URL")
    if parsed.query or parsed.fragment or parsed.path not in ("", "/") and not parsed.path[1:].isdigit():
        raise ValueError("Redis URL must have a numeric database and no query/fragment")
    port = parsed.port or 6379
    if not 1 <= port <= 65535:
        raise ValueError("invalid Redis port")
    # Retain URL-encoded credentials without ever printing them. The local proxy
    # verifies upstream TLS using the original hostname and system CA store.
    authority = parsed.netloc.rsplit("@", 1)[0] + "@" if "@" in parsed.netloc else ""
    return parsed.hostname, port, parsed.scheme == "rediss", authority, parsed.path or "/0"


class Case:
    def __init__(self, lab, name):
        self.lab, self.kind = lab, name
        self.name = f"two-host-{lab.run_id}-{name}"
        self.directory = lab.output / name
        self.directory.mkdir()
        self.clients, self.expected, self.candidates, self.contents = [], {}, [], {}
        self.private = {}
        self.sequence = 0
        self.samples = []
        self.client = None
        self.peer = Peer(lab.role, lab.args.control_port, lab.token, lab.args.timeout) if hasattr(lab, "peer") else None
        if self.peer:
            self.peer.session = lab.peer.session
            self.peer.scope = name

    def wait(self, label, predicate, sample=True):
        deadline = time.monotonic() + self.lab.args.timeout
        while time.monotonic() < deadline:
            if predicate():
                return
            time.sleep(.05)
        raise TimeoutError(label)

    def barrier(self, label, data=None):
        return self.peer.exchange(self.kind + ":" + label, data)

    def payload(self, label, size=257):
        key = f"{self.lab.settings['seed']}:{self.kind}:{label}".encode()
        return hashlib.shake_256(key).digest(size)

    def new_client(self, name):
        client = Client(self, name)
        client.config.write_text(json.dumps({
            "redis": f"redis://{self.lab.redis_auth}127.0.0.1:{client.proxy.port}{self.lab.redis_db}",
            "sync": {"watcherQueueCapacity": self.lab.settings["watcher_queue"]}}))
        client.config.chmod(0o600)
        client.root.mkdir(mode=0o755)
        return client

    def event(self, **fields):
        with (self.directory / "operations.jsonl").open("a") as log:
            log.write(json.dumps({"time": time.time(), **fields}) + "\n")

    def write(self, path, data, mode=0o644, atomic=True):
        full = self.client.root / path
        full.parent.mkdir(parents=True, exist_ok=True)
        if atomic:
            temporary = full.with_name(".afs-sync.tmp.two-host")
            with temporary.open("wb") as stream:
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
            temporary.chmod(mode)
            temporary.replace(full)
        else:
            with full.open("wb") as stream:
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
            full.chmod(mode)

    def parents(self, path):
        for parent in reversed(Path(path).parents):
            if str(parent) != ".":
                self.expected.setdefault(parent.as_posix(), {"type": "directory", "mode": 0o755})

    def file(self, role, path, label, size=257, mode=0o644, atomic=True, data=None):
        data = self.payload(label, size) if data is None else data
        value = {"type": "file", "size": len(data), "sha256": digest(data), "mode": mode}
        self.parents(path)
        self.contents[path], self.expected[path] = data, value
        self.event(action="write", writer=role, path=path, atomic=atomic, expected=value)
        if self.lab.role == role:
            self.write(path, data, mode, atomic)
        return value

    def directory_path(self, role, path, mode=0o755):
        self.parents(path)
        self.expected[path] = {"type": "directory", "mode": mode}
        if self.lab.role == role:
            full = self.client.root / path
            full.mkdir(parents=True, exist_ok=True)
            full.chmod(mode)

    def link(self, role, path, target):
        self.parents(path)
        value = {"type": "symlink", "target": target}
        self.expected[path] = value
        if self.lab.role == role:
            full = self.client.root / path
            full.parent.mkdir(parents=True, exist_ok=True)
            temporary = full.with_name(".afs-sync.tmp.two-host")
            temporary.symlink_to(target)
            temporary.replace(full)
        self.event(action="symlink", writer=role, path=path, expected=value)
        return value

    def delete(self, role, path):
        for name in list(self.expected):
            if name == path or name.startswith(path + "/"):
                del self.expected[name]
                self.contents.pop(name, None)
        if self.lab.role == role:
            full = self.client.root / path
            if full.is_dir() and not full.is_symlink():
                shutil.rmtree(full)
            else:
                full.unlink()
        self.event(action="delete", writer=role, path=path)

    def rename(self, role, source, target):
        self.parents(target)
        for path in list(self.expected):
            if path == source or path.startswith(source + "/"):
                renamed = target + path[len(source):]
                self.expected[renamed] = self.expected.pop(path)
                if path in self.contents:
                    self.contents[renamed] = self.contents.pop(path)
        if self.lab.role == role:
            (self.client.root / source).rename(self.client.root / target)
        self.event(action="rename", writer=role, source=source, target=target)

    def versions(self, paths, values, canonical=True):
        for path in paths:
            self.expected.pop(path, None)
        self.candidates.append({"paths": paths, "values": values, "canonical": canonical})

    def converge(self, label, client=None, local_only=False):
        client = client or self.client
        started = time.monotonic()
        stable = None
        prior = None
        self.sequence += 1
        artifact = self.directory / f"check-{self.sequence:03d}.json"
        save_json(self.directory / "oracle.json", {"exact": self.expected, "candidates": self.candidates,
                                                   "private_local": self.private})
        while time.monotonic() - started < self.lab.args.timeout:
            try:
                actual = tree(client.root)
                private = self.private if client is self.client else {}
                public = {p: v for p, v in actual.items() if p not in private}
                problems = differences(public, self.expected, self.candidates)
                problems += differences({p: actual[p] for p in private if p in actual}, private, [])
                sample = {"manifest": public, "problems": problems}
            except (OSError, AssertionError) as error:
                sample = {"manifest": {}, "problems": [{"scan_error": str(error)}]}
            sample["stable_for"] = time.monotonic() - stable if stable is not None else 0
            if local_only:
                values = {self.lab.role: sample}
            else:
                values = self.barrier("check:" + label, sample)
            fingerprints = [digest(json.dumps(s["manifest"], sort_keys=True).encode()) for s in values.values()]
            ok = all(not s["problems"] for s in values.values()) and len(set(fingerprints)) == 1
            now = time.monotonic()
            current = fingerprints[0] if ok else None
            unchanged = ok and current == prior
            if unchanged:
                stable = stable or now
            else:
                stable = None
            prior = current
            save_json(artifact, {"label": label, "seconds": now - started, "systems": values})
            # Both hosts make the same decision from the same exchanged samples;
            # independent wall clocks/timer cutoffs must never split a barrier.
            if unchanged and all(s["stable_for"] >= self.lab.settings["settle"]
                                              for s in values.values()):
                self.samples.append({"label": label, "seconds": now - started})
                return sample["manifest"]
            time.sleep(.2)
        raise AssertionError(f"{label}: did not converge; inspect {artifact.name}")

    def flush(self, label):
        for role in ("a", "b"):
            if self.lab.role == role:
                self.client.run("cp", "create", self.name, "--name", f"{label}-{role}")
            self.barrier("flush:" + label + ":" + role)

    def setup(self):
        self.client = self.new_client("writer-" + self.lab.role)
        if self.lab.role == "a":
            self.client.run("create", self.name)
        self.barrier("created")
        identities = self.barrier("workspace identity", self.client.run("info", self.name))
        if identities["a"]["id"] != identities["b"]["id"]:
            raise RuntimeError("systems reached different Redis workspaces/databases")
        if self.kind == "ignore":
            self.prepare_private()
        self.client.mount()
        self.barrier("mounted")

    def prepare_private(self):
        local = {".afsignore": b"private/\n*.local-only\n",
                 "private/keep.txt": self.payload(self.lab.role + " private"),
                 "notes.local-only": self.payload(self.lab.role + " ignored"),
                 ".DS_Store": self.payload(self.lab.role + " metadata")}
        for path, data in local.items():
            self.write(path, data)
            self.private[path] = {"type": "file", "size": len(data), "sha256": digest(data), "mode": 0o644}
        self.private["private"] = {"type": "directory", "mode": 0o755}

    def basic(self):
        for role in ("a", "b"):
            for suffix, size in (("empty", 0), ("binary", 8193), (".hidden", 91),
                                 ("spaces and # [brackets] 雪.txt", 311)):
                self.file(role, f"{role}/{suffix}", role + suffix, size)
            self.file(role, f"{role}/executable", role + "exec", mode=0o755)
            self.file(role, f"{role}/readonly", role + "read", mode=0o444)
            self.directory_path(role, f"{role}/empty-dir", 0o750)
            self.directory_path(role, f"{role}/deep/one/two/three")
            self.link(role, f"{role}/relative-link", "binary")
            self.link(role, f"{role}/dangling-link", "missing")
            self.link(role, f"{role}/directory-link", "deep")
        self.converge("bidirectional files, names, links and modes")
        # Have the other machine modify each writer's data.
        for role, owner in (("b", "a"), ("a", "b")):
            self.file(role, f"{owner}/binary", role + " reply", 4097, atomic=False)
            self.link(role, f"{owner}/relative-link", ".hidden")
        self.converge("reverse-direction edits and retargets")

    def mutations(self):
        for role in ("a", "b"):
            self.file(role, f"{role}/nested/source", role + " source", 8192)
            self.file(role, f"{role}/remove-me", role + " remove")
            self.link(role, f"{role}/link", "nested/source")
        self.converge("mutation baseline")
        for role in ("a", "b"):
            self.rename(role, f"{role}/nested", f"{role}/moved")
            self.delete(role, f"{role}/remove-me")
            self.delete(role, f"{role}/link")
        self.converge("recursive rename and deletion")
        for role in ("a", "b"):
            self.file(role, f"{role}/remove-me", role + " remove")
            self.link(role, f"{role}/link", "nested/source")
            path = f"{role}/moved/source"
            extra = self.payload(role + " append", 901)
            combined = self.contents[path] + extra
            self.contents[path] = combined
            self.expected[path].update(size=len(combined), sha256=digest(combined))
            if self.lab.role == role:
                with (self.client.root / path).open("ab") as stream:
                    stream.write(extra)
                    stream.flush()
                    os.fsync(stream.fileno())
        self.converge("identical recreation and append")
        for role in ("a", "b"):
            path = f"{role}/moved/source"
            self.contents[path] = self.contents[path][:7]
            self.expected[path].update(size=7, sha256=digest(self.contents[path]), mode=0o600)
            self.expected[f"{role}/moved"]["mode"] = 0o750
            if self.lab.role == role:
                with (self.client.root / path).open("r+b") as stream:
                    stream.truncate(7)
                (self.client.root / path).chmod(0o600)
                (self.client.root / role / "moved").chmod(0o750)
        self.converge("truncate and chmod")
        for role in ("a", "b"):
            self.delete(role, f"{role}/moved")
        self.converge("recursive removal")

    def bulk(self):
        for round_number in range(self.lab.settings["rounds"]):
            for role in ("a", "b"):
                for number in range(self.lab.settings["files"]):
                    path = f"{role}/r{round_number}/f{number:05d}"
                    self.file(role, path, path, (0, 97, 4097, 32771)[number % 4])
            self.converge(f"bulk round {round_number} create")
            for role in ("a", "b"):
                for number in range(self.lab.settings["files"]):
                    path = f"{role}/r{round_number}/f{number:05d}"
                    if number % 3 == 0:
                        self.delete(role, path)
                    elif number % 3 == 1:
                        self.rename(role, path, path + "-renamed")
                    else:
                        self.file(role, path, path + " edited", 501, atomic=False)
            self.converge(f"bulk round {round_number} mutate")

    def large(self):
        size = self.lab.settings["large_mib"] * 1024 * 1024 + 317
        for role in ("a", "b"):
            self.file(role, role + ".bin", role + " large", size)
        self.converge("large/chunked initial publication")
        for role in ("a", "b"):
            path = role + ".bin"
            data = bytearray(self.contents[path])
            patches = []
            for offset in (0, 256 * 1024 - 31, 1024 * 1024 - 17, size - 101):
                patch = self.payload(role + str(offset), 79)
                data[offset:offset + len(patch)] = patch
                patches.append((offset, patch))
            self.contents[path] = bytes(data)
            self.expected[path]["sha256"] = digest(data)
            if self.lab.role == role:
                with (self.client.root / path).open("r+b") as stream:
                    for offset, patch in patches:
                        stream.seek(offset)
                        stream.write(patch)
                    stream.flush()
                    os.fsync(stream.fileno())
        self.converge("in-place edits across chunk boundaries")
        for length in (1024 * 1024 - 1, 0, size + 65537):
            for role in ("a", "b"):
                self.file(role, role + ".bin", role + str(length), length, atomic=False)
            self.converge(f"large truncate/regrow {length}")

    def ignore(self):
        for role in ("a", "b"):
            self.file(role, role + "-public", role + " public")
        self.converge("ignored files stay private")
        self.flush("ignore-before-restart")
        self.client.run("unmount", self.client.root)
        self.client.process.wait(10)
        self.barrier("ignored restart")
        self.client.mount()
        self.converge("ignored files survive warm mount")

    def shared(self, existing=False, links=False):
        for iteration in range(self.lab.settings["rounds"]):
            path = f"shared-{iteration}"
            if existing:
                self.file("a", path, "baseline")
                self.converge("shared baseline " + str(iteration))
                self.flush("shared-baseline-" + str(iteration))
            # Stop and observe both daemons before either local write. This tests
            # true competing candidates instead of relying on clock synchronization.
            self.client.stop()
            self.barrier("both stopped " + str(iteration))
            values = []
            for role in ("a", "b"):
                if links:
                    values.append(self.link(role, path, role + "-target-" + str(iteration)))
                else:
                    values.append(self.file(role, path, role + str(iteration), 1048893 if existing else 911))
            self.versions([path], values)
            self.barrier("both wrote " + str(iteration))
            self.client.resume()
            self.converge("every competing version " + str(iteration))

    def recovery(self, crash=False):
        self.file("a", "shared", "baseline")
        self.link("a", "pointer", "baseline")
        self.converge("recovery baseline")
        self.flush("baseline")
        if self.lab.role == "b":
            if crash:
                self.client.stop()
            else:
                self.client.proxy.partition(True)
        self.barrier("B offline")
        local = self.file("b", "shared", "B unpublished")
        local_link = self.link("b", "pointer", "B-unpublished")
        self.file("b", "B-only", "new while offline")
        if crash and self.lab.role == "b":
            self.client.kill()
        self.barrier("B wrote")
        remote = self.file("a", "shared", "A published")
        remote_link = self.link("a", "pointer", "A-published")
        self.file("a", "A-only", "new while B offline")
        if self.lab.role == "a":
            # B's unpublished candidate must not be expected on A until reconnect.
            extra = self.expected.pop("B-only")
            self.converge("A continues while B offline", local_only=True)
            self.client.run("cp", "create", self.name, "--name", "offline-A")
            self.expected["B-only"] = extra
        elif not crash:
            self.wait("B reports disconnected", lambda:
                self.client.run("status", self.client.root)[0].get("sync", {}).get("connected") is False)
        self.barrier("A published during outage")
        self.versions(["shared"], [local, remote])
        self.versions(["pointer"], [local_link, remote_link])
        if self.lab.role == "b":
            if crash:
                self.client.mount()
            else:
                self.client.proxy.partition(False)
        self.barrier("B recovered")
        self.converge("all offline and peer versions recovered")

    def competing_mutation(self, rename=False):
        self.file("a", "source", "baseline")
        self.converge("competing mutation baseline")
        self.flush("baseline")
        if self.lab.role == "b":
            self.client.stop()
        self.barrier("B stopped")
        edited = self.file("b", "source", "B edit")
        self.barrier("B edited")
        if rename:
            # A's source is still the baseline; derive that version independently.
            baseline = self.payload("baseline")
            self.expected["source"] = {"type": "file", "size": len(baseline),
                                        "sha256": digest(baseline), "mode": 0o644}
            self.rename("a", "source", "destination")
        else:
            self.delete("a", "source")
        if self.lab.role == "a":
            self.converge("A mutation published", local_only=True)
            self.client.run("cp", "create", self.name, "--name", "A-mutated")
        self.barrier("A mutation complete")
        if rename:
            original = self.expected.pop("destination")
            self.versions(["source", "destination"], [original, edited], canonical=False)
        else:
            self.versions(["source"], [edited], canonical=False)
        if self.lab.role == "b":
            self.client.resume()
        self.converge("competing edit survives mutation")

    def finish(self):
        self.flush("final")
        self.converge("after completed flush receipts")
        cold = self.new_client("cold-" + self.lab.role)
        cold.mount()
        self.barrier("cold mounted")
        self.converge("fresh state and directory hydrate exact published tree", cold)
        cold.run("unmount", cold.root)
        cold.process.wait(10)
        self.barrier("cold stopped")
        self.client.run("unmount", self.client.root)
        self.client.process.wait(10)
        self.barrier("writers stopped")
        self.converge("unmount retains exact local tree")

    def run(self):
        self.setup()
        if self.kind == "shared-create":
            self.shared()
        elif self.kind == "shared-edit":
            self.shared(existing=True)
        elif self.kind == "symlink-conflict":
            self.shared(links=True)
        elif self.kind in ("partition", "crash"):
            self.recovery(crash=self.kind == "crash")
        elif self.kind in ("delete-edit", "rename-edit"):
            self.competing_mutation(rename=self.kind == "rename-edit")
        else:
            getattr(self, self.kind)()
        self.finish()

    def close(self):
        errors = []
        for client in reversed(self.clients):
            try:
                if client.process and client.process.poll() is None:
                    client.resume()
                    if not client.proxy.blocked:
                        try:
                            client.run("status", client.root, timeout=5)
                        except Exception as error:
                            self.event(action="status-capture-error", error=str(error))
                client.close()
            except Exception as error:
                errors.append(f"{client.name}: {error}")
        return errors


class Runner:
    def __init__(self, args):
        self.args = args
        self.role = "a" if args.command == "host" else "b"
        self.output = Path(args.output).resolve() if args.output else Path(tempfile.mkdtemp(prefix="afs-two-host-"))
        if args.output:
            self.output.mkdir(parents=True, exist_ok=False)
        self.output.chmod(0o700)
        self.binary = Path(args.binary).expanduser().resolve()
        self.token = secrets.token_hex(24) if self.role == "a" else os.environ.get("AFS_TEST_TOKEN", "")
        self.run_id = uuid.uuid4().hex[:12]
        self.gateway, self.server, self.peer = None, None, None
        self.report = {"schema_version": 1, "role": self.role, "platform": platform.platform(),
                       "hostname": socket.gethostname(), "python": sys.version,
                       "output": str(self.output), "cases": [], "cleanup_errors": [], "success": False}

    def save(self):
        save_json(self.output / "report.json", self.report)
        lines = ["# AFS two-system sync", "", f"System {self.role.upper()}: {self.report['platform']}", "",
                 f"Result: {'PASS' if self.report['success'] else 'FAIL / INCOMPLETE'}", "",
                 "| Scenario | Result | Seconds |", "|---|---|---:|"]
        for result in self.report["cases"]:
            lines.append(f"| {result['name']} | {result['status']} | {result['seconds']:.2f} |")
        for result in self.report["cases"]:
            if result.get("error"):
                lines += ["", f"**{result['name']}:** {result['error']}"]
        if self.report.get("error"):
            lines += ["", self.report["error"]]
        if self.report["cleanup_errors"]:
            lines += ["", "Cleanup errors: " + "; ".join(self.report["cleanup_errors"])]
        (self.output / "report.md").write_text("\n".join(lines) + "\n")

    def setup(self):
        if not self.binary.is_file() or not os.access(self.binary, os.X_OK):
            raise RuntimeError(f"AFS binary is not executable: {self.binary}; build it before running")
        if not shutil.which("ps"):
            raise RuntimeError("ps is required for observed daemon suspension")
        self.report["binary_sha256"] = digest(self.binary.read_bytes())
        self.report["source_sha256"] = source_hash()
        self.report["afs_version"] = subprocess.check_output([str(self.binary), "--version"], text=True, timeout=10).strip()
        if self.role == "a":
            self.settings = {"protocol": PROTOCOL, "run_id": self.run_id, "seed": self.args.seed,
                             "files": self.args.files, "rounds": self.args.rounds,
                             "large_mib": self.args.large_mib, "settle": self.args.settle,
                             "watcher_queue": self.args.watcher_queue,
                             "scenarios": self.args.scenario or list(SCENARIOS)}
            self.control = Coordination(self.token, self.settings)
            self.server = serve_control(self.control, self.args.control_port)
            print(f"\nOn B, open an SSH tunnel (replace USER@SYSTEM_A):\n"
                  f"  ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 "
                  f"-L {self.args.control_port}:127.0.0.1:{self.args.control_port} "
                  f"USER@SYSTEM_A\n\n"
                  f"In another terminal on B, from its AFS checkout:\n"
                  f"  AFS_TEST_TOKEN={self.token} python3 scripts/two_system_sync.py join "
                  f"--binary /tmp/afs-sync-test --control-port {self.args.control_port} "
                  f"--timeout {self.args.timeout}\n"
                  "  # Set AFS_TEST_REDIS on B too; see docs/two-system-sync.md.\n", flush=True)
        elif not self.token or len(self.token) != 48 or any(c not in "0123456789abcdef" for c in self.token):
            raise RuntimeError("set AFS_TEST_TOKEN to the token printed by system A")
        self.peer = Peer(self.role, self.args.control_port, self.token, self.args.timeout)
        self.settings = self.peer.call("hello", source=source_hash())
        if self.settings["protocol"] != PROTOCOL:
            raise RuntimeError("protocol mismatch")
        self.run_id = self.settings["run_id"]
        self.report["settings"] = self.settings
        self.report["retained_workspaces"] = [f"two-host-{self.run_id}-{name}" for name in self.settings["scenarios"]]
        print(f"Waiting for system {'B' if self.role == 'a' else 'A'} (up to {self.args.join_timeout}s)...", flush=True)
        raw_url = os.environ.get("AFS_TEST_REDIS", "")
        if not raw_url:
            raise RuntimeError("set AFS_TEST_REDIS to your existing Redis URL on each system")
        host, port, tls, self.redis_auth, self.redis_db = redis_endpoint(raw_url)
        self.gateway = Proxy(port, 0, host=host, tls=ssl.create_default_context() if tls else None)
        self.port = self.gateway.port
        self.report["redis_endpoint"] = {"host": host, "port": port, "tls": tls, "database": self.redis_db}
        self.report["systems"] = self.peer.exchange("joined", {
            "platform": self.report["platform"], "hostname": self.report["hostname"],
            "binary_sha256": self.report["binary_sha256"], "afs_version": self.report["afs_version"]},
            timeout=self.args.join_timeout)

    def run(self):
        print(f"System {self.role.upper()} artifacts: {self.output}", flush=True)
        try:
            self.setup()
            for name in self.settings["scenarios"]:
                print(f"RUN  {name}", flush=True)
                case = Case(self, name)
                started = time.monotonic()
                result = {"name": name, "status": "fail"}
                self.report["cases"].append(result)
                try:
                    case.run()
                    result["status"] = "pass"
                except Exception as error:
                    result["error"] = str(error) or type(error).__name__
                    (case.directory / "failure.txt").write_text(traceback.format_exc())
                    case.peer.call("abort-case", error=result["error"])
                finally:
                    result["seconds"] = time.monotonic() - started
                    result["samples"] = case.samples
                    self.report["cleanup_errors"].extend(case.close())
                    self.save()
                # Both peers finish their owned process cleanup before advancing.
                outcomes = self.peer.exchange("case done:" + name, {"status": result["status"],
                                   "error": result.get("error"), "cleanup_errors": self.report["cleanup_errors"]})
                result["systems"] = outcomes
                if any(s["status"] != "pass" for s in outcomes.values()):
                    result["status"] = "fail"
                self.save()
                print(f"{result['status'].upper()} {name} ({result['seconds']:.1f}s)", flush=True)
            summaries = self.peer.exchange("complete", {"cleanup_errors": self.report["cleanup_errors"]})
            if any(s["cleanup_errors"] for s in summaries.values()):
                raise RuntimeError("one system reported cleanup errors")
            self.report["success"] = all(r["status"] == "pass" for r in self.report["cases"])
            if self.role == "a":
                if not self.control.join_departed.wait(self.args.timeout):
                    raise TimeoutError("system B did not acknowledge completion")
            else:
                self.peer.call("goodbye")
        except BaseException as error:
            self.report["error"] = str(error) or type(error).__name__
            (self.output / "failure.txt").write_text(traceback.format_exc())
            if self.peer:
                try:
                    self.peer.call("abort", error=self.report["error"])
                except Exception:
                    pass
            print("FAIL " + self.report["error"], file=sys.stderr, flush=True)
            self.report["success"] = False
        finally:
            if self.gateway:
                try:
                    self.gateway.close()
                except Exception as error:
                    self.report["cleanup_errors"].append(str(error))
                    self.report["success"] = False
            if self.server:
                self.server.shutdown()
                self.server.server_close()
            self.save()
        print(f"{'PASS' if self.report['success'] else 'FAIL'}: {self.output / 'report.md'}", flush=True)
        return 0 if self.report["success"] else 1


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    subparsers = parser.add_subparsers(dest="command", required=True)
    for command in ("host", "join"):
        sub = subparsers.add_parser(command)
        sub.add_argument("--binary", required=True, help="existing AFS binary; never builds/replaces the installed command")
        sub.add_argument("--output", help="new artifact directory (default: fresh temporary directory)")
        sub.add_argument("--control-port", type=int, default=18765, help="loopback coordinator on A / SSH forwarded control on B")
        sub.add_argument("--timeout", type=float, default=120, help="per command, barrier, or convergence deadline in seconds")
        sub.add_argument("--join-timeout", type=float, default=900, help="initial peer wait in seconds")
        sub.add_argument("--latency-ms", type=float, default=0, help="delay per Redis proxy read, each direction on this host")
        if command == "host":
            sub.add_argument("--seed", type=int, default=1)
            sub.add_argument("--files", type=int, default=100, help="files per writer per bulk round")
            sub.add_argument("--rounds", type=int, default=3, help="bulk/conflict iterations")
            sub.add_argument("--large-mib", type=int, default=8)
            sub.add_argument("--settle", type=float, default=2, help="continuous matching observation window in seconds")
            sub.add_argument("--watcher-queue", type=int, default=1024, help="AFS watcher capacity; use a small value to stress recovery")
            sub.add_argument("--scenario", choices=SCENARIOS, action="append")
    args = parser.parse_args()
    if not 0 < args.control_port < 65536:
        parser.error("control-port must be in 1..65535")
    if not all(math.isfinite(n) for n in (args.timeout, args.join_timeout, args.latency_ms)) or args.timeout <= 0 or args.join_timeout <= 0 or args.latency_ms < 0:
        parser.error("timeouts must be positive; latency must be nonnegative")
    if args.command == "host":
        if min(args.files, args.rounds, args.large_mib) < 1 or args.large_mib < 2:
            parser.error("files/rounds must be positive and large-mib must be at least 2")
        if not math.isfinite(args.settle) or args.settle < .2 or args.timeout <= args.settle or not 1 <= args.watcher_queue <= 1048576:
            parser.error("settle must be >= 0.2 and below timeout; watcher-queue must be in 1..1048576")
        if args.scenario and len(set(args.scenario)) != len(args.scenario):
            parser.error("do not repeat a scenario")
    return args


def main():
    if os.name != "posix":
        raise SystemExit("This runner requires macOS or Linux.")
    # Private outputs/configs; workload permissions are explicit for cross-host checks.
    os.umask(0o022)
    def interrupt(*_):
        raise KeyboardInterrupt("interrupted")
    signal.signal(signal.SIGTERM, interrupt)
    args = parse_args()
    try:
        return Runner(args).run()
    except (OSError, ValueError) as error:
        print(f"Setup failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
