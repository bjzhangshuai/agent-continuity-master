from __future__ import annotations

import base64
import hashlib
import json
import os
import secrets
import tempfile
import uuid
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Mapping, Protocol

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey

from .client import Client


def _b64(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).decode().rstrip("=")


def _wire_json(value: Any, *, missing: bool = False) -> bytes:
    if missing:
        return b""
    return json.dumps(value, separators=(",", ":"), ensure_ascii=False).encode()


def _portable_string(value: str) -> str:
    return _b64(value.encode())


def _portable_strings(values: list[str]) -> str:
    return ".".join(_portable_string(value) for value in sorted(values))


def _epoch_millis(value: str | datetime) -> int:
    timestamp = value if isinstance(value, datetime) else datetime.fromisoformat(value.replace("Z", "+00:00"))
    return int(timestamp.timestamp() * 1000)


@dataclass(slots=True)
class DeviceIdentity:
    device_id: str
    name: str
    signing_private_key: str
    signing_public_key: str
    encryption_private_key: str
    encryption_public_key: str

    def to_dict(self) -> dict[str, str]:
        return asdict(self)


def generate_device_identity(name: str, device_id: str = "") -> DeviceIdentity:
    signing = Ed25519PrivateKey.generate()
    encryption = X25519PrivateKey.generate()
    private_format = {"encoding": serialization.Encoding.Raw, "format": serialization.PrivateFormat.Raw, "encryption_algorithm": serialization.NoEncryption()}
    return DeviceIdentity(
        device_id=device_id or f"device_{uuid.uuid4().hex}",
        name=name,
        signing_private_key=_b64(signing.private_bytes(**private_format)),
        signing_public_key=_b64(signing.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)),
        encryption_private_key=_b64(encryption.private_bytes(**private_format)),
        encryption_public_key=_b64(encryption.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)),
    )


def portable_event_signing_bytes(event: Mapping[str, Any]) -> bytes:
    logical = event["logical_time"]
    fields = [
        "AC-EVENT-1", _portable_string(str(event["event_id"])), _portable_string(str(event["task_id"])), _portable_string(str(event["branch_id"])),
        _portable_string(str(event["account_id"])), _portable_string(str(event.get("participant_id", ""))), _portable_string(str(event.get("agent_id", ""))),
        _portable_string(str(event.get("provider_id", ""))), _portable_string(str(event["device_id"])), _portable_string(str(event.get("session_ref", ""))),
        _portable_strings(list(event.get("parent_event_ids", []))), str(logical["wall_time_ms"]), str(logical.get("counter", 0)), _portable_string(str(logical["node"])),
        str(event["device_sequence"]), _portable_string(str(event["event_type"])), _portable_string(str(event["schema_version"])), _portable_string(str(event["content_type"])),
        _portable_string(str(event["content_digest"])), _b64(_wire_json(event.get("payload"), missing="payload" not in event)), _portable_strings(list(event.get("artifact_refs", []))),
        str(_epoch_millis(str(event["created_at_client"]))), _portable_string(str(event["idempotency_key"])), _b64(_wire_json(event.get("extensions"), missing="extensions" not in event)),
    ]
    return "\n".join(fields).encode()


def build_signed_event(*, identity: DeviceIdentity, account_id: str, task_id: str, event_type: str, payload: Any, sequence: int, parent_event_ids: list[str] | None = None, branch_id: str = "main", participant_id: str = "", agent_id: str = "", provider_id: str = "", session_ref: str = "", artifact_refs: list[str] | None = None, extensions: Any = None, created_at: datetime | None = None) -> dict[str, Any]:
    now = (created_at or datetime.now(timezone.utc)).astimezone(timezone.utc)
    event_id = f"event_{uuid.uuid4().hex}"
    payload_bytes = _wire_json(payload)
    event: dict[str, Any] = {
        "event_id": event_id, "task_id": task_id, "branch_id": branch_id, "account_id": account_id,
        "participant_id": participant_id, "agent_id": agent_id, "provider_id": provider_id, "device_id": identity.device_id,
        "session_ref": session_ref, "parent_event_ids": parent_event_ids or [],
        "logical_time": {"wall_time_ms": int(now.timestamp() * 1000), "counter": 0, "node": identity.device_id},
        "device_sequence": sequence, "event_type": event_type, "schema_version": "1.0", "content_type": "application/json",
        "content_digest": f"sha256:{hashlib.sha256(payload_bytes).hexdigest()}", "payload": payload, "artifact_refs": artifact_refs or [],
        "created_at_client": now.isoformat().replace("+00:00", "Z"), "signature": "", "idempotency_key": event_id,
    }
    if extensions is not None:
        event["extensions"] = extensions
    padding = "=" * (-len(identity.signing_private_key) % 4)
    private = Ed25519PrivateKey.from_private_bytes(base64.urlsafe_b64decode(identity.signing_private_key + padding))
    event["signature"] = f"ac1.{_b64(private.sign(portable_event_signing_bytes(event)))}"
    return event


def _portable_sync_hello_bytes(value: Mapping[str, Any]) -> bytes:
    tasks = sorted(({**task, "head_event_ids": sorted(set(task.get("head_event_ids", [])))} for task in value["tasks"]), key=lambda item: item["task_id"])
    fields = [
        "AC-SYNC-HELLO-1", _portable_strings(sorted(set(value["protocol_versions"]))), _portable_strings(sorted(set(value["capabilities"]))),
        _portable_string(str(value["client_version"])), _portable_string(str(value["device_id"])), _portable_string(str(value["nonce"])),
        str(_epoch_millis(str(value["timestamp"]))), str(len(tasks)),
    ]
    for task in tasks:
        fields.extend([_portable_string(str(task["task_id"])), _portable_strings(task["head_event_ids"]), str(task["event_cursor"])])
    return "\n".join(fields).encode()


def sign_sync_hello(identity: DeviceIdentity, tasks: list[Mapping[str, Any]], *, client_version: str = "python/0.1.0", now: datetime | None = None) -> dict[str, Any]:
    timestamp = (now or datetime.now(timezone.utc)).astimezone(timezone.utc)
    value: dict[str, Any] = {
        "protocol_versions": ["1.0"], "capabilities": ["batch_push", "cursor_pull", "missing_discovery", "artifact_manifests", "reconciliation", "e2ee"],
        "client_version": client_version, "device_id": identity.device_id, "nonce": _b64(secrets.token_bytes(24)),
        "timestamp": timestamp.isoformat().replace("+00:00", "Z"), "tasks": list(tasks),
    }
    padding = "=" * (-len(identity.signing_private_key) % 4)
    private = Ed25519PrivateKey.from_private_bytes(base64.urlsafe_b64decode(identity.signing_private_key + padding))
    value["proof"] = f"ac1.{_b64(private.sign(_portable_sync_hello_bytes(value)))}"
    return value


class KeyValueStore(Protocol):
    def get(self, key: str) -> str | None: ...
    def set(self, key: str, value: str) -> None: ...


class MemoryStore:
    def __init__(self) -> None:
        self.values: dict[str, str] = {}

    def get(self, key: str) -> str | None:
        return self.values.get(key)

    def set(self, key: str, value: str) -> None:
        self.values[key] = value


class FileStore:
    """Crash-safe desktop/server store. Keep device private keys in an OS keyring."""

    def __init__(self, directory: str | Path) -> None:
        self.directory = Path(directory)
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)

    def _path(self, key: str) -> Path:
        return self.directory / f"{hashlib.sha256(key.encode()).hexdigest()}.json"

    def get(self, key: str) -> str | None:
        path = self._path(key)
        return path.read_text() if path.exists() else None

    def set(self, key: str, value: str) -> None:
        fd, temporary = tempfile.mkstemp(prefix=".state-", dir=self.directory)
        try:
            os.fchmod(fd, 0o600)
            with os.fdopen(fd, "w") as output:
                output.write(value)
                output.flush()
                os.fsync(output.fileno())
            os.replace(temporary, self._path(key))
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)


class DeviceRuntime:
    def __init__(self, client: Client, identity: DeviceIdentity, account_id: str, store: KeyValueStore) -> None:
        self.client = client
        self.identity = identity
        self.account_id = account_id
        self.store = store
        self.key = f"runtime:{identity.device_id}"

    def _load(self) -> dict[str, Any]:
        raw = self.store.get(self.key)
        return json.loads(raw) if raw else {"sequence": 0, "outbox": [], "inbox": [], "tasks": {}}

    def _save(self, state: Mapping[str, Any]) -> None:
        self.store.set(self.key, json.dumps(state, separators=(",", ":"), ensure_ascii=False))

    def register(self) -> dict[str, Any]:
        return self.client.register_device(self.identity.to_dict())

    def queue_event(self, task_id: str, event_type: str, payload: Any, **options: Any) -> dict[str, Any]:
        state = self._load()
        state["sequence"] += 1
        task = state["tasks"].setdefault(task_id, {"cursor": 0, "head_event_ids": []})
        options.setdefault("parent_event_ids", task["head_event_ids"])
        event = build_signed_event(identity=self.identity, account_id=self.account_id, task_id=task_id, event_type=event_type, payload=payload, sequence=state["sequence"], **options)
        state["outbox"].append(event)
        task["head_event_ids"] = [event["event_id"]]
        self._save(state)
        return event

    def pending_events(self) -> list[dict[str, Any]]:
        return self._load()["outbox"]

    def received_events(self) -> list[dict[str, Any]]:
        return self._load()["inbox"]

    def sync_once(self) -> dict[str, int]:
        state = self._load()
        report = {"pushed": 0, "duplicates": 0, "rejected": 0, "pulled": 0, "acknowledged": 0}
        if state["outbox"]:
            response = self.client.sync_push(self.identity.device_id, state["outbox"][:100])
            accepted = {item["event_id"] for item in response["acknowledgements"] if item["accepted"]}
            report["pushed"] = len(accepted)
            report["duplicates"] = sum(1 for item in response["acknowledgements"] if item["accepted"] and item.get("duplicate"))
            report["rejected"] = sum(1 for item in response["acknowledgements"] if not item["accepted"])
            state["outbox"] = [event for event in state["outbox"] if event["event_id"] not in accepted]
        task_states = [{"task_id": task_id, "head_event_ids": value["head_event_ids"], "event_cursor": value["cursor"]} for task_id, value in state["tasks"].items()]
        hello = self.client.sync_hello(sign_sync_hello(self.identity, task_states))
        for plan in hello["tasks"]:
            task_id = plan["task"]["task_id"]
            local = state["tasks"].setdefault(task_id, {"cursor": 0, "head_event_ids": []})
            while local["cursor"] < plan["server_cursor"]:
                pulled = self.client.sync_pull(self.identity.device_id, [{"task_id": task_id, "after": local["cursor"], "limit": 200, "include_payload": True}])
                batch = pulled["batches"][0] if pulled["batches"] else None
                if not batch or batch["next_cursor"] <= local["cursor"]:
                    break
                state["inbox"].extend(batch["events"])
                local["cursor"] = batch["next_cursor"]
                local["head_event_ids"] = batch["task"]["head_event_ids"]
                report["pulled"] += len(batch["events"])
                if not batch["has_more"]:
                    break
        acknowledgements = [{"task_id": task_id, "cursor": value["cursor"]} for task_id, value in state["tasks"].items() if value["cursor"] > 0]
        if acknowledgements:
            self.client.sync_ack(self.identity.device_id, acknowledgements)
            report["acknowledged"] = len(acknowledgements)
        self._save(state)
        return report
