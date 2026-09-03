import base64
import unittest
from agent_continuity import Client

class ArchiveContractTest(unittest.TestCase):
    def test_binary_content_retry_identity_and_status(self):
        client = Client("http://local.test", "credential")
        calls = []
        def request(method, path, body=None):
            calls.append((method, path, body))
            return {"jobs": [], "next_after": 0, "has_more": False}
        client.request = request
        data = bytes([0, 1, 127, 128, 255])
        client.upload_content("task/x", "image", "v1", "截图.png", "image/png", data)
        self.assertEqual(calls[0][1], "/v1/tasks/task%2Fx/contents")
        self.assertEqual(calls[0][2]["version_id"], "v1")
        self.assertEqual(base64.b64decode(calls[0][2]["content_base64"]), data)
        client.list_storage_jobs("task/x", 42)
        self.assertTrue(calls[1][1].endswith("storage-jobs?after=42"))
        client.get_storage_job("task/x", 7)
        client.retry_storage_job("task/x", 7)
        self.assertEqual(calls[3][0], "POST")
        with self.assertRaises(ValueError):
            client.upload_content("t", "a", "v", "f", "text/plain", bytes(8*1024*1024+1))
