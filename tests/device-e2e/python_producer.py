from __future__ import annotations

import json
import os

from agent_continuity import Client, DeviceRuntime, MemoryStore, generate_device_identity

base_url = os.environ["CONTINUITY_E2E_URL"]
token = os.environ["CONTINUITY_E2E_TOKEN"]
account_id = os.environ["CONTINUITY_E2E_ACCOUNT_ID"]

client = Client(base_url, token)
identity = generate_device_identity("Python laptop")
runtime = DeviceRuntime(client, identity, account_id, MemoryStore())
runtime.register()
task = client.create_task("Cross-device SDK E2E", "Python creates; TypeScript continues", origin_device=identity.device_id)
event = runtime.queue_event(task["task_id"], "human.message.created", {"text": "来自 Python 设备的上下文", "progress": 45})
report = runtime.sync_once()
assert report["pushed"] == 1
print(json.dumps({"task_id": task["task_id"], "event_id": event["event_id"], "device_id": identity.device_id}, separators=(",", ":")))
