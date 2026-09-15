"""Checks that the lab rejects false convergence and cleans up failed startup."""

import importlib.util
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location("multiwriter_lab", Path(__file__).resolve().parents[2] / "scripts/multiwriter_lab.py")
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


class LabVerificationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="afs-lab-oracle-")
        self.addCleanup(self.temp.cleanup)
        self.parent = SimpleNamespace(output=Path(self.temp.name),
            args=SimpleNamespace(timeout=.1, latency_ms=0), port=1)
        self.case = lab.Case(self.parent, "shared-create")
        root = self.case.directory / "workspace"
        root.mkdir()
        self.client = SimpleNamespace(root=root, name="client")
        self.case.active = [self.client]

    def test_overwrite_cannot_change_intended_oracle(self):
        replace = Path.replace
        def competing_write(path, destination):
            result = replace(path, destination)
            Path(destination).write_bytes(b"unexpected competing bytes")
            return result
        with mock.patch.object(Path, "replace", competing_write):
            expected = self.case.write(self.client, "file", b"intended bytes")
        with self.assertRaisesRegex(AssertionError, "expected paths"):
            self.case.require({"file": expected})

    def test_flush_losing_all_copies_cannot_pass_cold_check(self):
        expected = self.case.write(self.client, "file", b"must survive flush")
        self.case.candidates("file", [expected])
        def lose_everywhere():
            (self.client.root / "file").unlink()
        self.case.flush = lose_everywhere
        with self.assertRaisesRegex(AssertionError, "candidate versions"):
            self.case.cold_check()

    def test_manifest_preserves_dangling_symlink_without_following(self):
        (self.client.root / "link").symlink_to("missing-target")
        self.assertEqual(lab.manifest(self.client.root),
                         {"link": {"type": "symlink", "target": "missing-target"}})

    def test_failed_foreground_start_is_reaped(self):
        # No Redis required: the owned process never advertises mount readiness.
        fake = self.parent.output / "not-ready"
        fake.write_text('#!/bin/sh\ncase "$*" in *unmount*) exit 1;; esac\nexec sleep 30\n')
        fake.chmod(0o700)
        self.parent.binary = fake
        client = lab.Client(self.case, "slow-start")
        try:
            with self.assertRaisesRegex(AssertionError, "mount readiness"):
                client.mount()
            process = client.process
        finally:
            client.close()
        self.assertIsNotNone(process.poll())


if __name__ == "__main__":
    unittest.main()
