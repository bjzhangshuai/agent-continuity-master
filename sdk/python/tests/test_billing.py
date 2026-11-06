import io
import json
import unittest
from unittest.mock import patch
from agent_continuity import Client


class BillingTest(unittest.TestCase):
    def test_refund_retries_preserve_identity_and_statement_is_text(self):
        seen = []
        def transport(request, **kwargs):
            self.assertEqual(request.get_header("Authorization"), "Bearer test-token")
            seen.append(request)
            return io.BytesIO(b"<html>receipt</html>" if request.full_url.endswith("/statement") else b'{"refunds":[]}')
        client = Client("http://billing.test", "test-token")
        with patch("agent_continuity.client.urlopen", transport):
            for _ in range(2):
                client.request_refund("inv/1", "pay1", 35, "unused", "stable-key")
            self.assertEqual(client.invoice_statement("inv/1"), "<html>receipt</html>")
        self.assertTrue(seen[0].full_url.endswith("/inv%2F1/refunds"))
        self.assertEqual(seen[0].get_header("Idempotency-key"), "stable-key")
        self.assertEqual(seen[0].data, seen[1].data)
        self.assertEqual(json.loads(seen[0].data)["amount_minor"], 35)
