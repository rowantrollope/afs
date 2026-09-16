#!/usr/bin/env python3
"""Isolated Linux FUSE acceptance for mount access/ownership options.

Run as root in the disposable native lab container. Owns a new Redis process,
AFS state directory and mountpoints; never connects to a supplied Redis server.
"""

import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--afs", required=True)
    parser.add_argument("--helper", required=True)
    args = parser.parse_args()
    if sys.platform != "linux" or os.geteuid() != 0 or not Path("/dev/fuse").exists():
        raise RuntimeError("requires root in an isolated Linux container with /dev/fuse")
    root = Path(tempfile.mkdtemp(prefix="afs-native-options-"))
    root.chmod(0o755)
    afs, helper = str(Path(args.afs).resolve()), str(Path(args.helper).resolve())
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    redis_log = open(root / "redis.log", "wb")
    redis = subprocess.Popen([
        "redis-server", "--bind", "127.0.0.1", "--port", str(port),
        "--save", "", "--appendonly", "no", "--dir", str(root),
    ], stdout=redis_log, stderr=subprocess.STDOUT)
    env = dict(os.environ, AFS_STATE_DIR=str(root / "state"), AFS_NATIVE_HELPER=helper)
    config = root / "config.json"
    config.write_text(json.dumps({"redis": f"redis://127.0.0.1:{port}/0"}))
    mounts = []
    acceptance = None

    def cli(*words):
        result = subprocess.run([afs, "--config", str(config), "--json", *words],
                                env=env, capture_output=True, text=True, timeout=45)
        if result.returncode:
            raise RuntimeError(f"afs {words}: {result.stdout} {result.stderr}")
        return json.loads(result.stdout)

    def nobody(code, *paths, uid=1000):
        def drop_privileges():
            os.setgroups([])
            os.setgid(uid)
            os.setuid(uid)
        result = subprocess.run([sys.executable, "-c", code, *map(str, paths)],
                                preexec_fn=drop_privileges, capture_output=True,
                                text=True, timeout=15)
        if result.returncode:
            raise RuntimeError(f"non-root assertion failed: {result.stdout} {result.stderr}")

    try:
        deadline = time.monotonic() + 10
        while True:
            if redis.poll() is not None:
                raise RuntimeError("disposable Redis exited")
            probe = subprocess.run(["redis-cli", "-h", "127.0.0.1", "-p", str(port), "--raw", "INFO", "server"],
                                   capture_output=True, text=True, timeout=2)
            if probe.returncode == 0:
                info = dict(line.split(":", 1) for line in probe.stdout.splitlines() if ":" in line)
                if int(info.get("process_id", -1)) != redis.pid:
                    raise RuntimeError("Redis port belongs to another process; refusing to use it")
                break
            if time.monotonic() > deadline:
                raise RuntimeError("disposable Redis readiness timed out")
            time.sleep(0.05)
        cli("create", "native-options")
        writer, reader, private = [root / name for name in ("writer", "reader", "private")]
        for path, readonly, shared in [(writer, False, True), (reader, True, True), (private, False, False)]:
            path.mkdir()
            options = ["--backend", "fuse", "--uid", "1000", "--gid", "1000"]
            if readonly:
                options += ["--readonly"]
            if shared:
                options += ["--allow-other"]
            # Register the attempted path first so a partial startup is cleaned.
            mounts.append(path)
            result = cli("mount", "native-options", str(path), *options)
            assert result["read_only"] == readonly, result
        nobody("""
import os, sys
from pathlib import Path
p = Path(sys.argv[1]) / 'nonroot.txt'
p.write_bytes(b'nonroot writer')
os.chmod(p, 0o640)
st = p.stat()
assert (st.st_uid, st.st_gid, st.st_mode & 0o777) == (1000, 1000, 0o640), st
assert p.read_bytes() == b'nonroot writer'
""", writer)
        nobody("""
import errno, os, sys
from pathlib import Path
root = Path(sys.argv[1]); p = root / 'nonroot.txt'
assert p.stat().st_uid == 1000
for operation in [lambda: p.read_bytes(), lambda: p.write_bytes(b'bad'), lambda: os.chmod(p, 0o777), lambda: p.unlink(), lambda: (root / 'forbidden').write_bytes(b'bad')]:
    try: operation()
    except OSError as e: assert e.errno in (errno.EACCES, errno.EPERM), e
    else: raise AssertionError('allow-other bypassed POSIX permissions')
""", writer, uid=2000)
        nobody("""
import errno, os, sys, time
from pathlib import Path
root = Path(sys.argv[1]); p = root / 'nonroot.txt'
deadline = time.monotonic() + 10
while True:
    try:
        assert p.read_bytes() == b'nonroot writer'
        break
    except (FileNotFoundError, AssertionError):
        if time.monotonic() > deadline: raise
        time.sleep(0.05)
st = p.stat()
assert (st.st_uid, st.st_gid) == (1000, 1000), st
for mutate in [lambda: p.write_bytes(b'bad'), lambda: (root / 'forbidden').write_bytes(b'bad'), lambda: os.chmod(p, 0o777), lambda: p.unlink()]:
    try: mutate()
    except OSError as e: assert e.errno in (errno.EROFS, errno.EACCES, errno.EPERM), e
    else: raise AssertionError('read-only mount allowed mutation')
""", reader)
        nobody("""
import errno, sys
from pathlib import Path
try: (Path(sys.argv[1]) / 'nonroot.txt').read_bytes()
except OSError as e: assert e.errno in (errno.EACCES, errno.EPERM), e
else: raise AssertionError('mount without allow-other admitted another uid')
""", private)
        assert (writer / "nonroot.txt").read_bytes() == b"nonroot writer"
        assert not (writer / "forbidden").exists()
        result = cli("unmount", str(reader))
        mounts.remove(reader)
        assert result["read_only"] and not result["synchronized"], result
        for path in list(reversed(mounts)):
            cli("unmount", str(path))
            mounts.remove(path)
        acceptance = {"success": True, "backend": "fuse", "uid": 1000, "gid": 1000,
                      "checks": ["nonroot-write-read-chmod", "ownership", "allow-other",
                                 "other-user-permissions", "default-private-access", "readonly-denies-mutations", "normal-unmount"]}
    finally:
        errors = []
        for path in reversed(mounts):
            try:
                cli("unmount", str(path), "--force")
            except Exception as error:
                errors.append(str(error))
        redis.terminate()
        try:
            redis.wait(timeout=10)
        except subprocess.TimeoutExpired:
            redis.kill()
            redis.wait(timeout=5)
        redis_log.close()
        if errors:
            print(f"cleanup failed; retained {root}: {errors}", file=sys.stderr)
            if sys.exc_info()[0] is None:
                raise RuntimeError("native acceptance cleanup failed")
        else:
            shutil.rmtree(root)
    print(json.dumps(acceptance))


if __name__ == "__main__":
    main()
