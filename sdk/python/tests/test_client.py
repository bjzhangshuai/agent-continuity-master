import io
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from agent_continuity import APIError, Client, DeviceRuntime, MemoryStore, build_signed_event, generate_device_identity, portable_event_signing_bytes
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
import base64


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/v1/tasks/task_1/permissions":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"permissions": [{"task_id": "task_1", "account_id": "acct_2", "permission": "read"}]}).encode())
        elif self.path.startswith("/v1/tasks"):
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"tasks": [{"task_id": "task_1"}]}).encode())
        else:
            self.send_response(404)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"error":{"code":"not_found","message":"missing"}}')

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length) or b"{}")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        if self.path == "/v1/auth/sso/exchange" and body == {"id_token": "oidc-token"}:
            self.wfile.write(b'{"access_token":"access","refresh_token":"refresh"}')
        elif self.path == "/v1/tasks/task_1/qr-invitations" and body["task_id"] == "task_1":
            self.wfile.write(b'{"qr_content":"agentcontinuity://invite?token=invite","qr_svg":"<svg/>"}')
        else:
            self.wfile.write(b'{}')

    def do_PUT(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length) or b"{}")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"task_id": "task_1", "account_id": "acct_2", "permission": body["permission"]}).encode())

    def do_DELETE(self):
        self.send_response(204)
        self.end_headers()

    def log_message(self, *_):
        pass


class ClientTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def test_list_and_typed_error(self):
        client = Client(f"http://127.0.0.1:{self.server.server_port}", "token")
        self.assertEqual(client.list_tasks()[0]["task_id"], "task_1")
        with self.assertRaises(APIError) as raised:
            client.health()
        self.assertEqual(raised.exception.code, "not_found")

    def test_qr_permission_and_sso_contracts(self):
        client = Client(f"http://127.0.0.1:{self.server.server_port}", "token")
        qr = client.create_qr_invitation({"version": 1, "task_id": "task_1", "token": "invite"})
        self.assertTrue(qr["qr_svg"].startswith("<svg"))
        self.assertEqual(client.list_task_permissions("task_1")[0]["permission"], "read")
        self.assertEqual(client.put_task_permission("task_1", "acct_2", "write")["permission"], "write")
        client.delete_task_permission("task_1", "acct_2")
        self.assertEqual(client.exchange_sso("oidc-token")["access_token"], "access")

    def test_portable_signature_and_offline_queue(self):
        identity = generate_device_identity("Python")
        event = build_signed_event(identity=identity, account_id="acct_1", task_id="task_1", event_type="progress.updated", payload={"text": "你好 <world>"}, sequence=1)
        padding = "=" * (-len(event["signature"][4:]) % 4)
        signature = base64.urlsafe_b64decode(event["signature"][4:] + padding)
        public_padding = "=" * (-len(identity.signing_public_key) % 4)
        public_key = Ed25519PublicKey.from_public_bytes(base64.urlsafe_b64decode(identity.signing_public_key + public_padding))
        public_key.verify(signature, portable_event_signing_bytes(event))
        runtime = DeviceRuntime(Client("http://127.0.0.1:1"), identity, "acct_1", MemoryStore())
        runtime.queue_event("task_1", "progress.updated", {"percent": 25})
        self.assertEqual(len(runtime.pending_events()), 1)


if __name__ == "__main__":
    unittest.main()
