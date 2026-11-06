import json
from pathlib import Path
import tempfile
import unittest

import bridge


class TranscriptTest(unittest.TestCase):
    def test_codebuddy_index_extracts_visible_text_only(self):
        with tempfile.TemporaryDirectory() as root:
            session = "session_test"
            base = Path(root) / session
            (base / "messages").mkdir(parents=True)
            (base / "index.json").write_text(json.dumps({"messages": ["u1", "a1"]}))
            (base / "messages/u1.json").write_text(json.dumps({
                "id": "u1", "role": "user", "message": {"role": "user", "content": "expanded secret"},
                "extra": json.dumps({"sourceContentBlocks": [{"type": "text", "text": "公开问题"}, {"type": "image", "data": "not copied"}], "reasoning": "not copied"})}))
            (base / "messages/a1.json").write_text(json.dumps({
                "id": "a1", "role": "assistant", "message": json.dumps({"role": "assistant", "content": [{"type": "thinking", "thinking": "private"}, {"type": "text", "text": "公开答案"}]})}))
            rows = bridge.read_transcript(base / "index.json", session, [root])
            self.assertEqual(rows, [{"native_id": "u1", "role": "user", "text": "公开问题"},
                                    {"native_id": "a1", "role": "assistant", "text": "公开答案"}])
            self.assertNotIn("private", json.dumps(rows))
            self.assertNotIn("expanded secret", json.dumps(rows))

    def test_path_escape_and_wrong_session_are_rejected(self):
        with tempfile.TemporaryDirectory() as root, tempfile.TemporaryDirectory() as outside:
            path = Path(outside) / "index.json"
            path.write_text('{"messages":[]}')
            with self.assertRaisesRegex(ValueError, "授权"):
                bridge.read_transcript(path, "session_test", [root])
            local = Path(root) / "another" / "index.json"
            local.parent.mkdir(); local.write_text('{"messages":[]}')
            with self.assertRaisesRegex(ValueError, "不一致"):
                bridge.read_transcript(local, "session_test", [root])

    def test_jsonl_requires_same_session_name(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "s1.jsonl"
            path.write_text('{"id":"1","role":"user","content":"hello"}\n')
            self.assertEqual(bridge.read_transcript(path, "s1", [root])[0]["text"], "hello")
            with self.assertRaisesRegex(ValueError, "同名"):
                bridge.read_transcript(path, "s2", [root])


if __name__ == "__main__":
    unittest.main()
