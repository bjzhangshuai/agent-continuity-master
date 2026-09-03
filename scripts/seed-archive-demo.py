"""Local-only SDK E2E. Uses existing demo login, never prints its credentials."""
import base64
import hashlib
import http.cookiejar
import json
from pathlib import Path
import sys
import time
from urllib.request import build_opener, HTTPCookieProcessor, install_opener, Request

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "sdk/python/src"))
from agent_continuity import Client, generate_device_identity, build_signed_event

BASE = "http://127.0.0.1:18080"
jar = http.cookiejar.CookieJar()
opener = build_opener(HTTPCookieProcessor(jar))
install_opener(opener)

class DemoClient(Client):
    def request(self, method, path, body=None, *, extra_headers=None):
        return super().request(method, path, body, extra_headers={"X-Portal-Request": "1", **(extra_headers or {})})

def main():
    demo = json.loads((ROOT / ".data/tenant-demo.json").read_text())
    user = demo["users"]["group"]
    client = DemoClient(BASE)
    client.request("POST", "/v1/auth/login", {"username": user["username"], "password": user["password"]})
    title = "对象存储 Demo · 对话、文件与图片"
    tasks = client.list_tasks(200)
    task = next((t for t in tasks if t["title"] == title), None)
    if task is None:
        task = client.create_task(title, "验证 SDK → 事务 Outbox → NATS JetStream → 独立 Worker → 私有对象存储 → 用量", organization_id=demo["tenants"]["group"], metadata={"demo": True, "feature": "object-archive"})
    task_id = task["task_id"]
    png = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII=")
    samples = [
        ("conversation", "对话摘录.md", "text/markdown", "# 客服对话\n用户：如何将 Claude 任务切换到手机？\n助手：上传任务状态后，在目标设备取回连续性包。\n".encode()),
        ("image", "界面截图.png", "image/png", png),
        ("report", "运营报告.txt", "text/plain", "本演示使用本地对象目标，不上传真实对话、不访问真实云账号。\n".encode()),
        ("manifest", "任务清单.json", "application/json", json.dumps({"device":"Mac + iPhone","agents":["Claude","CodeBuddy"],"queue":"NATS JetStream","simulated":True}, ensure_ascii=False).encode()),
        ("binary", "二进制附件.bin", "application/octet-stream", bytes(range(256))),
    ]
    for artifact_id, filename, mime, content in samples:
        client.upload_content(task_id, artifact_id, "archive-demo-v1", filename, mime, content)
    events = client.list_events(task_id)
    if not events:
        identity = generate_device_identity("对象归档演示 · Python SDK")
        client.register_device(identity.to_dict())
        event = build_signed_event(identity=identity, account_id=user["account_id"], task_id=task_id, event_type="progress.updated", payload={"text":"对话事件与附件都由持久化消息触发归档","demo":True}, sequence=1)
        client.append_event(event)
    deadline = time.monotonic() + 40
    while True:
        jobs = client.list_storage_jobs(task_id)["jobs"]
        if len(jobs) >= 6 and all(j["state"] == "stored" for j in jobs):
            break
        if time.monotonic() >= deadline:
            raise RuntimeError("Archive did not complete: " + str([(j["id"], j["state"], j.get("error_code")) for j in jobs]))
        time.sleep(1)
    for job in jobs:
        req = Request(f"{BASE}/v1/tasks/{task_id}/storage-jobs/{job['id']}/content", headers={"X-Portal-Request":"1"})
        with opener.open(req, timeout=20) as response:
            content = response.read()
        assert "sha256:" + hashlib.sha256(content).hexdigest() == job["content_hash"]
        assert len(content) == job["size"]
    print(json.dumps({"task_id":task_id,"title":title,"login":user["username"],"stored_jobs":len(jobs),"verified_downloads":len(jobs),"target":"local-demo-v1","real_cloud":False},ensure_ascii=False))

if __name__ == "__main__":
    main()
