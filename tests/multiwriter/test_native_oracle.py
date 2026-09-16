from pathlib import Path
import json
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[2] / "scripts"
sys.path.insert(0, str(SCRIPTS))
from native_multiwriter_lab import NativeCase


class NativeOracleTests(unittest.TestCase):
    def make_case(self):
        temp = tempfile.TemporaryDirectory(prefix="afs-native-oracle-")
        self.addCleanup(temp.cleanup)
        case = object.__new__(NativeCase)
        case.directory = Path(temp.name)
        case.active = [SimpleNamespace(root=case.directory / str(i), backend="nfs")
                       for i in range(3)]
        case.expectations = {}
        # Exercise one complete predicate without changing real lab deadlines.
        case.wait = lambda label, ready: self.assertTrue(ready(), label)
        return case

    def assert_parallel_collection(self, operation):
        case = self.make_case()
        barrier = threading.Barrier(len(case.active))
        visited, lock = [], threading.Lock()
        tree = {"file": {"type": "file", "size": 1, "sha256": "exact", "mode": 0o640}}

        def read(root):
            with lock:
                visited.append(root)
            # A sequential implementation cannot complete this barrier.
            barrier.wait(timeout=2)
            return dict(tree)

        with patch("native_multiwriter_lab.manifest", side_effect=read), \
                patch("native_multiwriter_lab.sys.platform", "darwin"):
            operation(case, tree)
        self.assertCountEqual(visited, [c.root for c in case.active])

    def test_require_collects_each_full_manifest_concurrently(self):
        self.assert_parallel_collection(lambda case, tree: case.require(tree))

    def test_same_collects_each_full_manifest_concurrently(self):
        self.assert_parallel_collection(lambda case, tree: self.assertTrue(case.same()))

    def test_require_keeps_exact_path_oracles_on_every_selected_client(self):
        case = self.make_case()
        expected = {"file": {"type": "file", "size": 4, "sha256": "exact", "mode": 0o640},
                    "empty": {"type": "directory", "mode": 0o700},
                    "link": {"type": "symlink", "target": "missing"}, "deleted": None}
        baseline = {p: value for p, value in expected.items() if value is not None}
        mutations = [
            {"file": None},
            {"file": {**expected["file"], "sha256": "wrong bytes"}},
            {"file": {**expected["file"], "mode": 0o644}},
            {"empty": None},
            {"link": {"type": "symlink", "target": "other"}},
            {"deleted": expected["file"]},
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                changed = dict(baseline)
                for path, value in mutation.items():
                    if value is None:
                        changed.pop(path, None)
                    else:
                        changed[path] = value
                def read(root):
                    return changed if root == case.active[-1].root else baseline
                with patch("native_multiwriter_lab.manifest", side_effect=read):
                    with self.assertRaisesRegex(AssertionError, "expected paths"):
                        case.require(expected)
                    # An explicitly selected subset retains its original scope.
                    case.require(expected, clients=case.active[:-1])
                self.assertEqual(json.loads((case.directory / "expected.json").read_text()), expected)

    def test_linux_same_does_not_ignore_extra_sidecars(self):
        case = self.make_case()
        baseline = {"file": {"type": "file", "sha256": "exact"}}
        def read(root):
            return {**baseline, "._file": {"type": "file", "sha256": "metadata"}} \
                if root == case.active[-1].root else baseline
        with patch("native_multiwriter_lab.sys.platform", "linux"), \
                patch("native_multiwriter_lab.manifest", side_effect=read):
            self.assertFalse(case.same())

    def test_manifest_failure_is_not_treated_as_convergence(self):
        case = self.make_case()
        with patch("native_multiwriter_lab.manifest", side_effect=RuntimeError("invalid tree")):
            with self.assertRaisesRegex(RuntimeError, "invalid tree"):
                case.require({"file": None})

    def test_sync_policy_only_projects_darwin_appledouble(self):
        tree = {p: {"type": "file", "size": 1} for p in
                ("plain", ".hidden", "dir/._sidecar", "._parent/nested", "dir/file")}
        with patch("native_multiwriter_lab.sys.platform", "darwin"):
            self.assertEqual(set(NativeCase.sync_tree(tree)), {"plain", ".hidden", "dir/file"})
        with patch("native_multiwriter_lab.sys.platform", "linux"):
            self.assertEqual(NativeCase.sync_tree(tree), tree)

    def test_native_sidecar_bytes_and_ordinary_sync_bytes_remain_required(self):
        with tempfile.TemporaryDirectory() as temp:
            clients = []
            for index, backend in enumerate(("nfs", "nfs", "sync")):
                root = Path(temp) / str(index)
                root.mkdir()
                (root / "ordinary").write_bytes(b"intended bytes")
                if backend == "nfs":
                    (root / "._ordinary").write_bytes(b"native metadata bytes")
                clients.append(SimpleNamespace(root=root, backend=backend))
            case = object.__new__(NativeCase)
            case.active = clients
            with patch("native_multiwriter_lab.sys.platform", "darwin"):
                self.assertTrue(case.same())
                (clients[1].root / "._ordinary").write_bytes(b"damaged metadata")
                self.assertFalse(case.same())
                (clients[1].root / "._ordinary").write_bytes(b"native metadata bytes")
                (clients[2].root / "ordinary").unlink()
                self.assertFalse(case.same())


if __name__ == "__main__":
    unittest.main()
