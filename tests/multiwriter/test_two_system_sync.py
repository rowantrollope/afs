"""Independent-oracle and coordination regressions for the two-system runner."""
import concurrent.futures
import importlib.util
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[2] / "scripts"
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("two_system_sync", SCRIPTS / "two_system_sync.py")
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


def file_value(data=b"intended"):
    return {"type": "file", "mode": 0o644, "size": len(data), "sha256": lab.digest(data)}


class OracleTests(unittest.TestCase):
    def test_unanimous_loss_and_corruption_fail(self):
        expected = {"file": file_value()}
        self.assertTrue(lab.differences({}, expected, []))
        self.assertTrue(lab.differences({"file": file_value(b"corrupt")}, expected, []))

    def test_metadata_and_unexpected_paths_fail(self):
        wanted = file_value()
        wrong = dict(wanted, mode=0o600)
        self.assertTrue(lab.differences({"file": wrong}, {"file": wanted}, []))
        self.assertTrue(lab.differences({"file": wanted, "resurrected": wanted}, {"file": wanted}, []))

    def test_every_conflict_candidate_must_survive(self):
        a, b = file_value(b"A"), file_value(b"B")
        rules = [{"paths": ["file"], "values": [a, b]}]
        self.assertTrue(lab.differences({"file": a}, {}, rules))
        self.assertTrue(lab.differences({"file": a, "file.conflict-x": file_value(b"damaged")}, {}, rules))
        self.assertEqual(lab.differences({"file": a, "file.conflict-x": b}, {}, rules), [])
        self.assertTrue(lab.differences({"file.conflict-a": a, "file.conflict-b": b}, {}, rules))

    def test_delete_edit_can_preserve_only_conflict_name(self):
        value = file_value()
        rules = [{"paths": ["file"], "values": [value], "canonical": False}]
        self.assertEqual(lab.differences({"file.conflict-x": value}, {}, rules), [])
        self.assertTrue(lab.differences({}, {}, rules))

    def test_symlinks_are_not_followed_and_hidden_files_are_checked(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "empty").mkdir()
            (root / "link").symlink_to("missing")
            (root / "directory-link").symlink_to("empty")
            (root / ".hidden").write_bytes(b"data")
            (root / ".DS_Store").write_bytes(b"must be accounted for explicitly")
            actual = lab.tree(root)
            self.assertEqual(actual["link"], {"type": "symlink", "target": "missing"})
            self.assertEqual(actual["directory-link"], {"type": "symlink", "target": "empty"})
            self.assertIn(".hidden", actual)
            self.assertIn(".DS_Store", actual)

    def test_missing_root_is_not_an_empty_success(self):
        with self.assertRaisesRegex(AssertionError, "root is missing"):
            lab.tree(Path("/does-not-exist-afs-test"))

    def test_oracle_does_not_read_back_raced_write(self):
        with tempfile.TemporaryDirectory() as temp:
            runner = SimpleNamespace(output=Path(temp), run_id="test", role="a", settings={"seed": 1})
            case = lab.Case(runner, "basic")
            root = case.directory / "root"
            root.mkdir()
            case.client = SimpleNamespace(root=root)
            case.write = lambda path, data, *args: (root / path).write_bytes(b"peer overwrote intended data")
            value = case.file("a", "file", "original")
            self.assertEqual(value, file_value(case.payload("original")))
            self.assertTrue(lab.differences(lab.tree(root), case.expected, []))

    def test_convergence_cannot_pass_when_both_peers_lost_file(self):
        with tempfile.TemporaryDirectory() as temp:
            runner = SimpleNamespace(output=Path(temp), run_id="test", role="a",
                args=SimpleNamespace(timeout=.1), settings={"seed": 1, "settle": .01})
            case = lab.Case(runner, "basic")
            root = case.directory / "root"
            root.mkdir()
            case.client = SimpleNamespace(root=root)
            case.expected = {"file": file_value()}
            case.barrier = lambda label, value: {"a": value, "b": value}
            with self.assertRaisesRegex(AssertionError, "did not converge"):
                case.converge("lost everywhere")
            evidence = json.loads((case.directory / "check-001.json").read_text())
            self.assertTrue(evidence["systems"]["a"]["problems"])

    def test_valid_but_unpublished_conflict_names_do_not_converge(self):
        with tempfile.TemporaryDirectory() as temp:
            runner = SimpleNamespace(output=Path(temp), run_id="test", role="a",
                args=SimpleNamespace(timeout=.1), settings={"seed": 1, "settle": .01})
            case = lab.Case(runner, "shared-create")
            root = case.directory / "root"
            root.mkdir()
            for name, content in (("file", b"A"), ("file.conflict-a", b"B")):
                (root / name).write_bytes(content)
                (root / name).chmod(0o644)
            case.client = SimpleNamespace(root=root)
            case.versions(["file"], [file_value(b"A"), file_value(b"B")])
            def disagree(label, value):
                other = dict(value, manifest={"file": file_value(b"A"), "file.conflict-b": file_value(b"B")})
                return {"a": value, "b": other}
            case.barrier = disagree
            with self.assertRaisesRegex(AssertionError, "did not converge"):
                case.converge("unpublished conflicts")


class CoordinationTests(unittest.TestCase):
    def setUp(self):
        self.state = lab.Coordination("a" * 48, {"run_id": "test"})
        for role in ("a", "b"):
            self.state.request(dict(op="hello", role=role, session=role, source=lab.source_hash()))

    def send(self, role, label="phase", data=None, step=0):
        return self.state.request(dict(op="exchange", role=role, session=role, label=label, data=data, step=step))

    def test_barrier_requires_both_and_retries_are_idempotent(self):
        self.assertFalse(self.send("a", data=1)["ready"])
        self.assertFalse(self.send("a", data=1)["ready"])
        self.assertTrue(self.send("b", data=2)["ready"])
        self.assertEqual(self.send("a", data=1)["values"], {"a": 1, "b": 2})
        with self.assertRaisesRegex(ValueError, "changed its payload"):
            self.send("a", data=3)

    def test_phase_mismatch_aborts_instead_of_false_pass(self):
        self.send("a")
        with self.assertRaisesRegex(RuntimeError, "barrier mismatch"):
            self.send("b", label="different")
        with self.assertRaises(RuntimeError):
            self.send("a", step=1)

    def test_duplicate_peer_and_different_scripts_rejected(self):
        with self.assertRaisesRegex(ValueError, "occupied"):
            self.state.request(dict(op="hello", role="a", session="another", source=lab.source_hash()))
        with self.assertRaisesRegex(ValueError, "sources differ"):
            self.state.request(dict(op="hello", role="a", session="a", source="wrong"))

    def test_case_failure_releases_peer_and_next_case_can_run(self):
        self.state.request(dict(op="abort-case", role="a", session="a", scope="first", error="lost file"))
        with self.assertRaisesRegex(RuntimeError, "lost file"):
            self.state.request(dict(op="exchange", role="b", session="b", scope="first", step=0, label="wait"))
        for role in ("a", "b"):
            result = self.state.request(dict(op="exchange", role=role, session=role,
                                            scope="second", step=0, label="next"))
        self.assertTrue(result["ready"])

    def test_missing_peer_times_out(self):
        peer = lab.Peer("a", 1, "a" * 48, .05)
        peer.session = "a"
        peer.call = lambda op, **fields: self.state.request(dict(op=op, role="a", session="a", **fields))
        with self.assertRaisesRegex(TimeoutError, "coordination timeout"):
            peer.exchange("phase")

    def test_http_auth_and_two_peer_exchange(self):
        state = lab.Coordination("b" * 48, {"run_id": "test"})
        server = lab.serve_control(state, 0)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        port = server.server_address[1]
        bad = lab.Peer("a", port, "wrong", 1)
        with self.assertRaisesRegex(RuntimeError, "authentication"):
            bad.call("hello", source=lab.source_hash())
        peers = [lab.Peer(role, port, state.token, 2) for role in ("a", "b")]
        for peer in peers:
            peer.call("hello", source=lab.source_hash())
        with concurrent.futures.ThreadPoolExecutor(2) as pool:
            results = list(pool.map(lambda peer: peer.exchange("round", peer.role), peers))
        self.assertEqual(results, [{"a": "a", "b": "b"}] * 2)
        peers[1].call("abort", error="test failure")
        with self.assertRaisesRegex(RuntimeError, "system B failed"):
            peers[0].exchange("next")


class EndpointTests(unittest.TestCase):
    def test_remote_credentials_database_and_tls(self):
        self.assertEqual(lab.redis_endpoint("rediss://user:p%40ss@example.com:1234/5"),
                         ("example.com", 1234, True, "user:p%40ss@", "/5"))
        self.assertEqual(lab.redis_endpoint("redis://[::1]:6380"), ("::1", 6380, False, "", "/0"))
        for url in ("http://host", "redis://", "redis://host/not-a-db", "redis://host/0?insecure=true"):
            with self.assertRaises(ValueError):
                lab.redis_endpoint(url)

    def test_proxy_disconnects_existing_streams_and_reconnects(self):
        import socketserver
        class Echo(socketserver.BaseRequestHandler):
            def handle(self):
                try:
                    while True:
                        data = self.request.recv(1024)
                        if not data:
                            break
                        self.request.sendall(data)
                except OSError:
                    pass
        server = socketserver.ThreadingTCPServer(("127.0.0.1", 0), Echo)
        server.daemon_threads = True
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        proxy = lab.Proxy(server.server_address[1], 0, host="localhost")
        try:
            with socket.create_connection(("127.0.0.1", proxy.port), timeout=2) as connection:
                connection.sendall(b"first")
                self.assertEqual(connection.recv(1024), b"first")
                proxy.partition(True)
                self.assertEqual(connection.recv(1024), b"")
            with socket.create_connection(("127.0.0.1", proxy.port), timeout=2) as connection:
                self.assertEqual(connection.recv(1024), b"")
            proxy.partition(False)
            with socket.create_connection(("127.0.0.1", proxy.port), timeout=2) as connection:
                connection.sendall(b"second")
                self.assertEqual(connection.recv(1024), b"second")
        finally:
            proxy.close()
            server.shutdown()
            server.server_close()

    @unittest.skipUnless(shutil.which("openssl"), "openssl required for generated TLS test certificate")
    def test_remote_proxy_verifies_tls_and_original_hostname(self):
        import socketserver
        with tempfile.TemporaryDirectory() as temp:
            key, cert = Path(temp) / "key.pem", Path(temp) / "cert.pem"
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                            "-days", "1", "-subj", "/CN=localhost", "-addext",
                            "subjectAltName=DNS:localhost", "-keyout", str(key), "-out", str(cert)],
                           check=True, capture_output=True, timeout=15)
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(cert, key)
            class TLS(socketserver.ThreadingTCPServer):
                daemon_threads = True
                def get_request(self):
                    connection, address = super().get_request()
                    connection.settimeout(3)
                    try:
                        return context.wrap_socket(connection, server_side=True), address
                    except Exception:
                        connection.close()
                        raise
            class Echo(socketserver.BaseRequestHandler):
                def handle(self):
                    self.request.sendall(self.request.recv(1024))
            server = TLS(("127.0.0.1", 0), Echo)
            threading.Thread(target=server.serve_forever, daemon=True).start()
            try:
                trusted = ssl.create_default_context(cafile=str(cert))
                for host, tls, expected in (("localhost", trusted, b"tls"),
                                            ("127.0.0.1", trusted, b""),
                                            ("localhost", ssl.create_default_context(), b"")):
                    proxy = lab.Proxy(server.server_address[1], 0, host=host, tls=tls)
                    try:
                        with socket.create_connection(("127.0.0.1", proxy.port), timeout=3) as connection:
                            connection.sendall(b"tls")
                            self.assertEqual(connection.recv(1024), expected)
                    finally:
                        proxy.close()
            finally:
                server.shutdown()
                server.server_close()


if __name__ == "__main__":
    unittest.main()
