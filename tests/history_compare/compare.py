#!/usr/bin/env python3
"""Compare source-pinned original and current AFS using disposable Redis only."""
from __future__ import annotations

import argparse
import contextlib
import hashlib
import json
import os
import pathlib
import platform
import shutil
import socket
import statistics
import subprocess
import tarfile
import tempfile
import time

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parent.parent


def command(args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kwargs).stdout.strip()


def tree_hash(root, production_only=False):
    digest = hashlib.sha256()
    for path in sorted(root.rglob("*")):
        if path.is_file() and (path.suffix in (".go", ".lua") or path.name in ("go.mod", "go.sum")):
            if production_only and (path.name.endswith("_test.go") or "tests" in path.relative_to(root).parts):
                continue
            digest.update(str(path.relative_to(root)).encode() + b"\0")
            digest.update(path.read_bytes())
    return digest.hexdigest()


def snapshot_original(source, revision, destination):
    archive = destination.parent / "original.tar"
    with archive.open("wb") as output:
        subprocess.run(["git", "-C", str(source), "archive", revision], stdout=output, check=True)
    destination.mkdir()
    with tarfile.open(archive) as data:
        data.extractall(destination, filter="data")
    archive.unlink()


def snapshot_current(destination):
    destination.mkdir()
    files = subprocess.check_output(["git", "-C", str(ROOT), "ls-files", "-z", "--cached", "--others", "--exclude-standard"]).split(b"\0")
    for encoded in files:
        if not encoded:
            continue
        relative = pathlib.Path(os.fsdecode(encoded))
        if relative.parts[0] in ("bin", ".comparison"):
            continue
        source = ROOT / relative
        if not source.is_file() and not source.is_symlink():
            continue
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, target, follow_symlinks=False)


def build(kind, source, directory, env):
    driver = source / ".comparison"
    driver.mkdir()
    shutil.copy2(HERE / "driver.go.in", driver / "main.go")
    shutil.copy2(HERE / (kind + ".go.in"), driver / "adapter.go")
    binary = directory / (kind + "-comparison")
    completed = subprocess.run(["go", "build", "-o", str(binary), "./.comparison"], cwd=source, env=env, capture_output=True, text=True)
    (directory / (kind + "-build.log")).write_text(completed.stdout + completed.stderr)
    completed.check_returncode()
    return binary


@contextlib.contextmanager
def isolated_redis(executable, directory):
    directory.mkdir()
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    with (directory / "redis.log").open("wb") as log:
        server = subprocess.Popen([str(executable), "--bind", "127.0.0.1", "--port", str(port),
            "--save", "", "--appendonly", "no", "--dir", str(directory), "--loglevel", "warning",
            "--maxmemory", "0"], stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 10
            while True:
                if server.poll() is not None:
                    raise RuntimeError("disposable Redis exited; inspect " + str(directory / "redis.log"))
                try:
                    with socket.create_connection(("127.0.0.1", port), timeout=0.1) as connection:
                        # The ephemeral port was selected before spawn. Refuse
                        # to write if another listener won that narrow race.
                        connection.sendall(b"*2\r\n$4\r\nINFO\r\n$6\r\nserver\r\n")
                        stream=connection.makefile("rb")
                        header=stream.readline()
                        if not header.startswith(b"$"):
                            raise RuntimeError("cannot verify ownership of disposable Redis port")
                        info=stream.read(int(header[1:].strip()))
                        expected=f"process_id:{server.pid}\r\n".encode()
                        if expected not in info:
                            raise RuntimeError("Redis process ID does not match owned subprocess")
                        break
                except OSError:
                    if time.monotonic() >= deadline:
                        raise RuntimeError("disposable Redis readiness timed out")
                    time.sleep(0.02)
            yield "127.0.0.1:" + str(port)
        finally:
            server.terminate()
            try:
                server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()


def execute(binary, address, case, env, *, count=40, size=65536, expected=0):
    completed = subprocess.run([str(binary), "-addr", address, "-case", case, "-writes", str(count), "-size", str(size)],
        env=env, text=True, capture_output=True, timeout=120)
    if completed.returncode != expected:
        raise RuntimeError(f"driver {case} exited {completed.returncode}: {completed.stdout}\n{completed.stderr}")
    if expected:
        return None
    return json.loads(completed.stdout)


def aggregate(rows):
    groups = {}
    for row in rows:
        groups.setdefault((row["case"], row["implementation"]), []).append(row)
    metrics = ("median_us", "p95_us", "total_ms", "workspace_memory_usage_bytes",
               "historical_payload_memory_usage_bytes", "versions", "unavailable_versions")
    result = []
    for (case, implementation), values in sorted(groups.items()):
        complete = [v for v in values if "harness_error" not in v and not v.get("error")]
        item = {"case": case, "implementation": implementation, "runs": len(values),
                "successful_runs": len(complete), "recoverable_runs": sum(bool(v.get("recoverable")) for v in complete)}
        checks = sorted({name for value in complete for name in value.get("checks", {})})
        if checks:
            item["check_passing_runs"] = {name: sum(bool(value.get("checks", {}).get(name)) for value in complete) for name in checks}
        for metric in metrics:
            if complete:
                item[metric] = statistics.median(v[metric] for v in complete)
        result.append(item)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--original", required=True, type=pathlib.Path)
    parser.add_argument("--original-ref", default="HEAD")
    parser.add_argument("--output", required=True, type=pathlib.Path)
    parser.add_argument("--redis-server", default=shutil.which("redis-server"), type=pathlib.Path)
    parser.add_argument("--repetitions", default=3, type=int)
    parser.add_argument("--implementations", default="original,new", help="original,new or one implementation during development")
    parser.add_argument("--cases", default="overwrite,identical,metadata,retention,append,first_delete,delete_retention,large_cutoff,disable,crash,api,multiwriter")
    args = parser.parse_args()
    if args.repetitions < 1 or not args.redis_server:
        parser.error("positive repetitions and a Redis server executable are required")
    output = args.output.resolve()
    if output.exists():
        parser.error("--output must name a new directory")
    output.mkdir(parents=True)
    original = args.original.resolve()
    revision = command(["git", "-C", str(original), "rev-parse", args.original_ref])
    env = os.environ.copy()
    for name in ("AFS_REDIS_URL", "AFS_REDIS_PASSWORD", "AFS_E2E_BINARY"):
        env.pop(name, None)
    env["AFS_HISTORY_COMPARISON_ISOLATED"] = "1"
    env["GOCACHE"] = env.get("GOCACHE", str(pathlib.Path(tempfile.gettempdir()) / "afs-history-go-cache"))
    env["HOME"] = str(output / "private-home")
    pathlib.Path(env["HOME"]).mkdir()
    # Preserve the user's Go module cache for read-only dependency reuse. Source
    # builds and runtime state remain in the disposable comparison directory.
    env["GOMODCACHE"] = command(["go", "env", "GOMODCACHE"])
    original_copy, new_copy = output / "original-source", output / "new-source"
    snapshot_original(original, revision, original_copy)
    snapshot_current(new_copy)
    report = {"original_revision": revision, "new_base_revision": command(["git", "-C", str(ROOT), "rev-parse", "HEAD"]),
        "original_source_sha256": tree_hash(original_copy), "new_source_sha256": tree_hash(new_copy),
        "original_production_sha256": tree_hash(original_copy, True), "new_production_sha256": tree_hash(new_copy, True),
        "harness_sha256": {path.name:hashlib.sha256(path.read_bytes()).hexdigest() for path in sorted(HERE.iterdir()) if path.is_file() and path.suffix in (".py", ".in")},
        "redis": command([str(args.redis_server), "--version"]), "go": command(["go", "version"]),
        "host": platform.platform(), "repetitions": args.repetitions,
        "method": "Both implementations enabled; identical high-level client publications; fresh owned Redis per scenario, no persistence, localhost. MEMORY USAGE sums workspace keys after operations; original blob bodies and new history bodies are measured separately.",
        "crash_method": "Original observer exits after live publication before history observer runs; new exits immediately after atomic publication returns. This targets the original post-publication gap, not a random crash-rate measurement.",
        "rows": []}
    selected = args.implementations.split(",")
    if any(kind not in ("original", "new") for kind in selected):
        parser.error("implementations must be original,new or either one")
    binaries = {kind: build(kind, source, output, env) for kind, source in (("original", original_copy), ("new", new_copy)) if kind in selected}
    for repetition in range(args.repetitions):
        for case in args.cases.split(","):
            for kind in (("original", "new") if repetition % 2 == 0 else ("new", "original")):
                if kind not in binaries:
                    continue
                print(f"run {repetition+1}/{args.repetitions}: {case} {kind}", flush=True)
                try:
                    with isolated_redis(args.redis_server, output / f"redis-{repetition}-{case}-{kind}") as address:
                        if case == "crash":
                            execute(binaries[kind], address, "crash_seed", env)
                            execute(binaries[kind], address, "crash_write", env, expected=86)
                            row = execute(binaries[kind], address, "crash_inspect", env)
                        else:
                            row = execute(binaries[kind], address, case, env, count=16 if case == "append" else 40,
                                          size=1048576 if case == "append" else 65536)
                        row["case"] = case
                        row["repetition"] = repetition + 1
                except Exception as error:
                    row = {"case": case, "implementation": kind, "repetition": repetition+1, "harness_error": str(error)}
                report["rows"].append(row)
                report["aggregate"] = aggregate(report["rows"])
                (output / "results.json").write_text(json.dumps(report, indent=2) + "\n")
    print(output / "results.json")
    if any("harness_error" in row or row.get("error") for row in report["rows"]):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
