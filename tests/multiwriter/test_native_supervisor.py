"""Real process checks for native-lab deadlines and artifact-scoped cleanup."""

import importlib.util
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import signal
import sys
import tempfile
import time
import unittest
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[2] / "scripts"
spec = importlib.util.spec_from_file_location("native_lab_supervisor", SCRIPTS / "native_lab_supervisor.py")
supervisor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(supervisor)


class NativeSupervisorTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="afs-native-supervisor-test-")
        self.addCleanup(temp.cleanup)
        self.base = Path(temp.name).resolve()
        self.output = self.base / "lab"
        self.python = Path(sys.executable)

    def supervise(self, program, timeout=2):
        with contextlib.redirect_stdout(io.StringIO()):
            result = supervisor.supervise([sys.executable, "-c", program], self.output,
                                          self.python, self.python, timeout, .5)
        return result, json.loads((self.output / "supervisor.json").read_text())

    def stop_if_same(self, pid, identity):
        row = supervisor.process_table().get(pid)
        if row is not None and row[1:] == identity:
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass

    def test_blocked_workload_times_out_without_signalling_unrelated_process(self):
        unrelated = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"])
        try:
            started = time.monotonic()
            code, report = self.supervise("import time; time.sleep(30)", timeout=.2)
            self.assertLess(time.monotonic() - started, 5)
            self.assertEqual(code, 1)
            self.assertEqual(report["status"], "fail")
            self.assertTrue(report["timed_out"])
            self.assertEqual(report["remaining_owned_pids"], [])
            self.assertIsNone(unrelated.poll())
            self.assertIn("binary_sha256", report)
        finally:
            unrelated.kill()
            unrelated.wait(timeout=2)

    def test_completed_child_exit_is_preserved(self):
        for child_code in (0, 7):
            with self.subTest(exit=child_code):
                self.output = self.base / ("exit-" + str(child_code))
                code, report = self.supervise("raise SystemExit(" + str(child_code) + ")")
                self.assertEqual(code, int(child_code != 0))
                self.assertEqual(report["returncode"], child_code)
                self.assertFalse(report["timed_out"])
                self.assertEqual(report["status"], "fail" if child_code else "pass")

    def registry(self):
        base = self.output / "native-ranges" / "client-0"
        state = base / "state"
        state.mkdir(parents=True)
        record = {"backend": "nfs", "id": "a" * 32, "token": "test-only-token",
                  "local_path": str(base / "workspace"),
                  "runtime_dir": str(state / "native" / ("a" * 32))}
        registry = state / "mounts.json"
        registry.write_text(json.dumps({"version": 1, "mounts": [record]}))
        return base, state, registry, record

    def test_cleanup_uses_owned_registry_and_refuses_other_mounts(self):
        base, state, registry, record = self.registry()
        fake = self.base / "afs"
        fake.write_text('#!/bin/sh\nprintf "%s\\n" "$AFS_STATE_DIR" "$@" > "$AFS_STATE_DIR/calls"\n'
                        'printf \'{"mounts":[]}\' > "$AFS_STATE_DIR/mounts.json"\n')
        fake.chmod(0o700)
        results, errors = supervisor.detach_registered(self.output, fake, fake, 1)
        self.assertEqual(errors, [])
        self.assertEqual(len(results), 1)
        self.assertEqual((state / "calls").read_text().splitlines(),
                         [str(state), "--config", str(base / "config.json"), "--json",
                          "unmount", str(base / "workspace"), "--force"])
        (state / "calls").unlink()
        record["local_path"] = str(self.base / "unrelated-mount")
        registry.write_text(json.dumps({"mounts": [record]}))
        results, errors = supervisor.detach_registered(self.output, fake, fake, 1)
        self.assertEqual(results, [])
        self.assertTrue(errors)
        self.assertFalse((state / "calls").exists())

    def test_timed_out_cleanup_terminates_separate_session_child(self):
        child_pid = self.base / "child.pid"
        program = ("import pathlib,subprocess,sys,time; "
                   "p=subprocess.Popen([sys.executable,'-c','import time; time.sleep(30)'],start_new_session=True); "
                   "pathlib.Path(sys.argv[1]).write_text(str(p.pid)); time.sleep(30)")
        result = supervisor.bounded([sys.executable, "-c", program, str(child_pid)], 1,
                                    track_children=True)
        self.assertTrue(result["timed_out"])
        self.assertIsNotNone(result["returncode"])
        self.assertTrue(child_pid.exists(), "fixture did not start its detach-helper stand-in")
        pid = int(child_pid.read_text())
        row = supervisor.process_table().get(pid)
        if row is not None:
            self.addCleanup(self.stop_if_same, pid, row[1:])
        # A just-killed orphan can briefly remain a zombie; it must not be a
        # live sleeping helper. The supervisor records any such cleanup doubt.
        check = subprocess.run(["ps", "-o", "stat=", "-p", str(pid)], capture_output=True, text=True, timeout=2)
        self.assertTrue(not check.stdout.strip() or check.stdout.strip().startswith("Z"), check.stdout)

    def test_default_deadline_tracks_all_native_scenarios(self):
        with mock.patch.object(sys, "path", [str(SCRIPTS), *sys.path]):
            import native_multiwriter_lab
        with mock.patch.object(sys, "argv", ["native_lab_supervisor.py", "--output", str(self.output)]), \
                mock.patch.object(supervisor, "supervise", return_value=0) as run, \
                mock.patch.object(supervisor.signal, "signal"):
            self.assertEqual(supervisor.main(), 0)
        self.assertEqual(run.call_args.args[4], len(native_multiwriter_lab.SCENARIOS) * 720)


if __name__ == "__main__":
    unittest.main()
