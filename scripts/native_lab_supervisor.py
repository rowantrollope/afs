#!/usr/bin/env python3
"""Bound the native lab even when a kernel filesystem call never returns.

All remaining arguments go to native_multiwriter_lab.py. --overall-timeout
sets the workload deadline; --cleanup-timeout bounds each cleanup subprocess.
Timeouts and uncertain cleanup always fail. Artifacts are never removed.
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time


SCRIPT = Path(__file__).resolve().with_name("native_multiwriter_lab.py")


def option(arguments, name, default=None):
    result = default
    for index, value in enumerate(arguments):
        if value == name and index + 1 < len(arguments):
            result = arguments[index + 1]
        if value.startswith(name + "="):
            result = value.split("=", 1)[1]
    return result


def bounded(command, timeout, env=None, limit=128 * 1024, track_children=False):
    """Unlike subprocess.run(timeout), never wait indefinitely after SIGKILL."""
    with tempfile.TemporaryFile() as log:
        process = subprocess.Popen(command, env=env, stdout=log, stderr=subprocess.STDOUT,
                                   start_new_session=True)
        timed_out = False
        cleanup_errors = []
        known = {}
        try:
            process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            if track_children:
                try:
                    table = process_table()
                    descendants(table, process.pid, known)
                    for pid in reversed(list(known)):
                        if pid != process.pid and pid in table and table[pid][1:] == known[pid]:
                            try:
                                os.kill(pid, signal.SIGKILL)
                            except ProcessLookupError:
                                pass
                except Exception as error:
                    cleanup_errors.append("cleanup child inspection: " + str(error))
            process.kill()
            try:
                process.wait(timeout=min(2, timeout))
            except subprocess.TimeoutExpired:
                pass
        if known:
            try:
                table = process_table()
                remaining = [pid for pid, identity in known.items()
                             if pid in table and table[pid][1:] == identity]
                if remaining:
                    cleanup_errors.append("cleanup subprocesses still present: " + str(remaining))
            except Exception as error:
                cleanup_errors.append("cleanup child verification: " + str(error))
        log.seek(0)
        return {"returncode": process.poll(), "timed_out": timed_out, "pid": process.pid,
                "output": log.read(limit).decode(errors="replace"), "cleanup_errors": cleanup_errors}


def process_table():
    result = bounded(["ps", "-axo", "pid=,ppid=,lstart=,command="], 2, limit=16 * 1024 * 1024)
    if result["timed_out"] or result["returncode"] != 0:
        raise RuntimeError("cannot inspect process identities: " + result["output"])
    table = {}
    for line in result["output"].splitlines():
        fields = line.split(None, 7)
        if len(fields) == 8:
            table[int(fields[0])] = (int(fields[1]), " ".join(fields[2:7]), fields[7])
    return table


def descendants(table, root, known):
    # Keep creation time and command, not just PIDs: a later unrelated process
    # must never be signalled because an owned process's PID was reused.
    parents = {root} if root is not None else set()
    parents.update(pid for pid, identity in known.items()
                   if pid in table and table[pid][1:] == identity)
    while True:
        added = {pid for pid, row in table.items() if row[0] in parents} - parents
        if not added:
            break
        parents.update(added)
    for pid in parents:
        if pid in table:
            known[pid] = table[pid][1:]


def detach_registered(output, binary, timeout):
    results, errors = [], []
    # Fixed shallow layout: never walk a mounted workspace to find registries.
    for registry in output.glob("*/*/state/mounts.json"):
        base = registry.parent.parent
        try:
            if any(path.is_symlink() for path in (registry, registry.parent, base, base.parent)):
                raise ValueError("symlink in lab registry path")
            records = json.loads(registry.read_text()).get("mounts", [])
            for record in records:
                mountpoint = str(base / "workspace")
                if record.get("local_path") != mountpoint:
                    raise ValueError("registry mountpoint is outside this client's workspace")
                runtime = record.get("runtime_dir", "")
                if record.get("backend") in ("fuse", "nfs"):
                    identity = record.get("id", "")
                    if len(identity) != 32 or any(c not in "0123456789abcdef" for c in identity):
                        raise ValueError("invalid native runtime identity")
                    expected = str(base / "state" / "native" / identity)
                    if runtime != expected:
                        raise ValueError("native runtime is outside this client's state")
                if not record.get("token"):
                    raise ValueError("registry has no authenticated mount identity")
                env = {k: v for k, v in os.environ.items()
                       if not k.startswith("AFS_") and k not in ("HOME", "XDG_CONFIG_HOME")}
                env.update(HOME=str(base / "home"), XDG_CONFIG_HOME=str(base / "config"),
                           AFS_STATE_DIR=str(base / "state"))
                result = bounded([str(binary), "--config", str(base / "config.json"),
                                  "--json", "unmount", mountpoint, "--force"], timeout, env,
                                 track_children=True)
                result.update(registry=str(registry), mountpoint=mountpoint,
                              backend=record.get("backend", "sync"))
                results.append(result)
                if result["timed_out"] or result["returncode"] != 0:
                    errors.append("forced detach failed: " + mountpoint)
        except Exception as error:
            errors.append(str(registry) + ": " + str(error))
    return results, errors


def supervise(command, output, binary, overall_timeout, cleanup_timeout):
    report = {"schema_version": 2, "overall_timeout_seconds": overall_timeout,
              "cleanup_timeout_seconds": cleanup_timeout, "status": "running",
              "timed_out": False, "cleanup_errors": [], "detach_results": [],
              "output": str(output), "command": list(command),
              "binary": str(binary)}
    known = {}
    started = time.monotonic()
    logfile = output.parent / (output.name + ".supervisor.log")
    report["log"] = str(logfile)
    process = None
    try:
        with logfile.open("xb") as log:
            process = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT,
                                       start_new_session=True)
        report["pid"] = process.pid
        while process.poll() is None:
            descendants(process_table(), process.pid, known)
            if time.monotonic() - started >= overall_timeout:
                report["timed_out"] = True
                break
            time.sleep(min(0.5, max(0.01, overall_timeout - (time.monotonic() - started))))
        report["returncode"] = process.poll()
    except KeyboardInterrupt:
        report["interrupted"] = True
    except Exception as error:
        report["error"] = str(error)
    finally:
        if process is not None:
            # Freeze orchestration while taking its registry-based cleanup
            # snapshot. Native daemons are separate sessions and keep serving.
            if process.poll() is None:
                try:
                    process.send_signal(signal.SIGSTOP)
                except ProcessLookupError:
                    pass
            results, errors = detach_registered(output, binary, cleanup_timeout)
            report["detach_results"].extend(results)
            report["cleanup_errors"].extend(errors)
            try:
                table = process_table()
                descendants(table, process.pid if process.poll() is None else None, known)
                for pid, identity in known.items():
                    if pid in table and table[pid][1:] == identity:
                        try:
                            os.kill(pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                if process.poll() is None:
                    process.kill()
                try:
                    process.wait(timeout=cleanup_timeout)
                except subprocess.TimeoutExpired:
                    report["cleanup_errors"].append("lab process did not exit after SIGKILL")
                # Daemon death can leave an NFS/FUSE mount behind. Retry the
                # identity-checked CLI detach; never issue a generic umount.
                retry, retry_errors = detach_registered(output, binary, cleanup_timeout)
                report["detach_results"].extend(retry)
                report["cleanup_errors"].extend(retry_errors)
                after = process_table()
                remaining = [pid for pid, identity in known.items()
                             if pid in after and after[pid][1:] == identity]
                report["remaining_owned_pids"] = remaining
                if remaining:
                    report["cleanup_errors"].append("owned processes still present: " + str(remaining))
            except Exception as error:
                report["cleanup_errors"].append("process cleanup: " + str(error))
        failed = (report["timed_out"] or report.get("interrupted") or report.get("error") or
                  report.get("returncode") != 0 or report["cleanup_errors"])
        report["status"] = "fail" if failed else "pass"
        report["seconds"] = time.monotonic() - started
        try:
            report["binary_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
        except OSError as error:
            report["binary_hash_error"] = str(error)
        output.mkdir(parents=True, exist_ok=True)
        (output / "supervisor.json").write_text(json.dumps(report, indent=2) + "\n")
        print(f"{report['status'].upper()}: native lab supervisor; {output / 'supervisor.json'}", flush=True)
    return 1 if failed else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__, add_help=False)
    parser.add_argument("--overall-timeout", type=float)
    parser.add_argument("--cleanup-timeout", type=float, default=30)
    args, child_args = parser.parse_known_args()
    if "--help" in child_args or "-h" in child_args:
        parser.print_help()
        return subprocess.call([sys.executable, str(SCRIPT), "--help"])
    if not math.isfinite(args.cleanup_timeout) or args.cleanup_timeout <= 0 or (
            args.overall_timeout is not None and
            (not math.isfinite(args.overall_timeout) or args.overall_timeout <= 0)):
        parser.error("supervisor timeouts must be finite and positive")
    raw_output = option(child_args, "--output")
    if raw_output:
        output = Path(raw_output).resolve()
        if output.exists() or output.is_symlink():
            parser.error("--output must be a new artifact directory")
        output.parent.mkdir(parents=True, exist_ok=True)
    else:
        output = Path(tempfile.mkdtemp(prefix="afs-native-supervised-")) / "lab"
        child_args.extend(["--output", str(output)])
    binary = Path(option(child_args, "--binary", str(output / "afs"))).absolute()
    from native_multiwriter_lab import SCENARIOS
    scenarios = sum(value == "--scenario" or value.startswith("--scenario=") for value in child_args) or len(SCENARIOS)
    clients = int(option(child_args, "--clients", "4"))
    command_timeout = float(option(child_args, "--timeout", "90"))
    if clients < 2 or not math.isfinite(command_timeout) or command_timeout <= 0:
        parser.error("clients must be at least two and command timeout must be finite and positive")
    overall = args.overall_timeout or scenarios * max(600, (clients + 4) * command_timeout)
    if not math.isfinite(overall):
        parser.error("computed overall timeout must be finite; supply --overall-timeout")
    def interrupt(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupt)
    return supervise([sys.executable, str(SCRIPT), *child_args], output, binary,
                     overall, args.cleanup_timeout)


if __name__ == "__main__":
    sys.exit(main())
