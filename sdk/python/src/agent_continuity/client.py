from __future__ import annotations

import json
import base64
from dataclasses import dataclass
from typing import Any, BinaryIO, Mapping
from urllib.error import HTTPError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


@dataclass(slots=True)
class APIError(Exception):
    status: int
    code: str
    message: str

    def __str__(self) -> str:
        return f"API {self.status} {self.code}: {self.message}"


class Client:
    """Dependency-free REST client. Payloads use the versioned protocol JSON schema."""

    def upload_content(self, task_id: str, artifact_id: str, version_id: str, filename: str, content_type: str, content: bytes, parent_version_id: str = "") -> dict[str, Any]:
        """201 means accepted, not cloud-stored. Persist/reuse version_id for retries."""
        if len(content) > 8 * 1024 * 1024:
            raise ValueError("Use resumable upload for content larger than 8 MiB")
        return self.request("POST", f"/v1/tasks/{quote(task_id, safe='')}/contents", {"artifact_id": artifact_id, "version_id": version_id, "filename": filename, "content_type": content_type, "parent_version_id": parent_version_id, "content_base64": base64.b64encode(content).decode("ascii")})

    def list_storage_jobs(self, task_id: str, after: int = 0) -> dict[str, Any]:
        return self.request("GET", f"/v1/tasks/{quote(task_id, safe='')}/storage-jobs?{urlencode({'after': after})}")

    def get_storage_job(self, task_id: str, job_id: int) -> dict[str, Any]:
        return self.request("GET", f"/v1/tasks/{quote(task_id, safe='')}/storage-jobs/{job_id}")

    def retry_storage_job(self, task_id: str, job_id: int) -> dict[str, Any]:
        return self.request("POST", f"/v1/tasks/{quote(task_id, safe='')}/storage-jobs/{job_id}/retry")

    def __init__(self, base_url: str, token: str = "", timeout: float = 30.0) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = timeout

    def request(self, method: str, path: str, body: Any | None = None, *, extra_headers: Mapping[str, str] | None = None) -> Any:
        data = None if body is None else json.dumps(body, separators=(",", ":"), ensure_ascii=False).encode()
        headers = {"Accept": "application/json", "User-Agent": "agent-continuity-python/0.1.0", **(extra_headers or {})}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        if data is not None:
            headers["Content-Type"] = "application/json"
        request = Request(self.base_url + path, data=data, headers=headers, method=method)
        try:
            with urlopen(request, timeout=self.timeout) as response:
                raw = response.read()
        except HTTPError as error:
            raw = error.read()
            try:
                envelope = json.loads(raw)
                detail = envelope.get("error", {})
            except (ValueError, TypeError):
                detail = {}
            raise APIError(error.code, detail.get("code", "http_error"), detail.get("message", error.reason)) from error
        return None if not raw else json.loads(raw)

    def health(self) -> None:
        self.request("GET", "/v1/health/ready")

    def issue_token(self, bootstrap_token: str, account_id: str, subject: str) -> dict[str, Any]:
        return self.request("POST", "/v1/auth/token", {"account_id": account_id, "subject": subject, "ttl_seconds": 3600}, extra_headers={"X-Bootstrap-Token": bootstrap_token})

    def refresh_token(self, refresh_token: str) -> dict[str, Any]:
        return self.request("POST", "/v1/auth/refresh", {"refresh_token": refresh_token})

    def logout(self, refresh_token: str) -> None:
        self.request("POST", "/v1/auth/logout", {"refresh_token": refresh_token})

    def list_tasks(self, limit: int = 100) -> list[dict[str, Any]]:
        return self.request("GET", f"/v1/tasks?{urlencode({'limit': limit})}")["tasks"]

    def create_task(self, title: str, objective: str, *, origin_device: str = "", organization_id: str = "", team_id: str = "", metadata: Mapping[str, Any] | None = None) -> dict[str, Any]:
        return self.request("POST", "/v1/tasks", {"title": title, "objective": objective, "origin_device": origin_device, "organization_id": organization_id, "team_id": team_id, "metadata": metadata})

    def get_task(self, task_id: str) -> dict[str, Any]:
        return self.request("GET", f"/v1/tasks/{quote(task_id, safe='')}")

    def append_event(self, event: Mapping[str, Any]) -> dict[str, Any]:
        return self.request("POST", f"/v1/tasks/{quote(str(event['task_id']), safe='')}/events", event)

    def list_events(self, task_id: str, after: int = 0, include_payload: bool = True) -> list[dict[str, Any]]:
        query = urlencode({"after": after, "include_payload": str(include_payload).lower()})
        return self.request("GET", f"/v1/tasks/{quote(task_id, safe='')}/events?{query}")["events"]

    def continuity_package(self, task_id: str, target_agent: str = "generic") -> dict[str, Any]:
        return self.request("GET", f"/v1/tasks/{quote(task_id, safe='')}/continuity-package?{urlencode({'agent': target_agent})}")

    def create_invitation(self, task_id: str, permission: str, ttl_seconds: int = 900) -> str:
        result = self.request("POST", f"/v1/tasks/{quote(task_id, safe='')}/invitations", {"permission": permission, "ttl_seconds": ttl_seconds})
        return result["token"]

    def create_qr_invitation(self, invitation: Mapping[str, Any]) -> dict[str, Any]:
        task_id = str(invitation["task_id"])
        return self.request("POST", f"/v1/tasks/{quote(task_id, safe='')}/qr-invitations", invitation)

    def list_task_permissions(self, task_id: str) -> list[dict[str, Any]]:
        return self.request("GET", f"/v1/tasks/{quote(task_id, safe='')}/permissions")["permissions"]

    def put_task_permission(self, task_id: str, account_id: str, permission: str) -> dict[str, Any]:
        if permission not in {"read", "write", "admin"}:
            raise ValueError("permission must be read, write, or admin")
        path = f"/v1/tasks/{quote(task_id, safe='')}/permissions/{quote(account_id, safe='')}"
        return self.request("PUT", path, {"permission": permission})

    def delete_task_permission(self, task_id: str, account_id: str) -> None:
        path = f"/v1/tasks/{quote(task_id, safe='')}/permissions/{quote(account_id, safe='')}"
        self.request("DELETE", path)

    def exchange_sso(self, id_token: str) -> dict[str, Any]:
        return self.request("POST", "/v1/auth/sso/exchange", {"id_token": id_token})

    def accept_invitation(self, token: str) -> dict[str, Any]:
        return self.request("POST", "/v1/invitations/accept", {"token": token})

    def list_organizations(self) -> list[dict[str, Any]]:
        return self.request("GET", "/v1/organizations")["organizations"]

    def create_organization(self, name: str, slug: str, plan: str = "free", key_provider: str = "platform", data_residency: str = "", settings: Mapping[str, Any] | None = None) -> dict[str, Any]:
        return self.request("POST", "/v1/organizations", {"name": name, "slug": slug, "plan": plan, "key_provider": key_provider, "data_residency": data_residency, "settings": settings})

    def list_devices(self, include_revoked: bool = False) -> list[dict[str, Any]]:
        query = urlencode({"include_revoked": str(include_revoked).lower()})
        return self.request("GET", f"/v1/devices?{query}")["devices"]

    def register_device(self, identity: Mapping[str, Any]) -> dict[str, Any]:
        return self.request("POST", "/v1/devices", {"device_id": identity["device_id"], "name": identity["name"], "public_key": identity["signing_public_key"], "encryption_public_key": identity["encryption_public_key"]})

    def revoke_device(self, device_id: str) -> None:
        self.request("DELETE", f"/v1/devices/{quote(device_id, safe='')}")

    def list_agents(self) -> list[dict[str, Any]]:
        return self.request("GET", "/v1/agents")["agents"]

    def create_agent(self, name: str, provider: str, capabilities: list[str], *, model: str = "", card: Mapping[str, Any] | None = None) -> dict[str, Any]:
        return self.request("POST", "/v1/agents", {"name": name, "provider": provider, "model": model, "capabilities": capabilities, "card": card})

    def revoke_agent(self, agent_id: str) -> None:
        self.request("DELETE", f"/v1/agents/{quote(agent_id, safe='')}")

    def bind_task_agent(self, task_id: str, agent_id: str, resume_mode: str, capabilities: list[str], *, role: str = "contributor", native_session_ref: str = "", metadata: Mapping[str, Any] | None = None) -> dict[str, Any]:
        return self.request("POST", f"/v1/tasks/{quote(task_id, safe='')}/agents", {"agent_id": agent_id, "role": role, "native_session_ref": native_session_ref, "resume_mode": resume_mode, "capabilities": capabilities, "metadata": metadata})

    def list_task_agents(self, task_id: str) -> list[dict[str, Any]]:
        return self.request("GET", f"/v1/tasks/{quote(task_id, safe='')}/agents")["agent_bindings"]

    def sync_hello(self, value: Mapping[str, Any]) -> dict[str, Any]:
        return self.request("POST", "/v1/sync/hello", value)

    def sync_push(self, device_id: str, events: list[Mapping[str, Any]]) -> dict[str, Any]:
        return self.request("POST", "/v1/sync/push", {"device_id": device_id, "events": events})

    def sync_pull(self, device_id: str, tasks: list[Mapping[str, Any]]) -> dict[str, Any]:
        return self.request("POST", "/v1/sync/pull", {"device_id": device_id, "tasks": tasks})

    def sync_ack(self, device_id: str, tasks: list[Mapping[str, Any]]) -> None:
        self.request("POST", "/v1/sync/ack", {"device_id": device_id, "tasks": tasks})

    def list_conflicts(self, task_id: str, include_resolved: bool = False) -> list[dict[str, Any]]:
        query = urlencode({"include_resolved": str(include_resolved).lower()})
        return self.request("GET", f"/v1/tasks/{quote(task_id, safe='')}/conflicts?{query}")["conflicts"]

    def list_billing_accounts(self) -> list[dict[str, Any]]:
        return self.request("GET", "/v1/billing/accounts")["billing_accounts"]

    def create_billing_account(self, plan: str, currency: str = "USD", organization_id: str = "") -> dict[str, Any]:
        return self.request("POST", "/v1/billing/accounts", {"organization_id": organization_id, "plan": plan, "currency": currency})

    def create_marketplace_transaction(self, listing_id: str, idempotency_key: str, *, quantity: int = 1, buyer_agent_id: str = "", seller_agent_id: str = "", task_id: str = "", metadata: Mapping[str, Any] | None = None) -> dict[str, Any]:
        return self.request("POST", "/v1/marketplace/transactions", {"listing_id": listing_id, "idempotency_key": idempotency_key, "quantity": quantity, "buyer_agent_id": buyer_agent_id, "seller_agent_id": seller_agent_id, "task_id": task_id, "metadata": metadata})

    def transition_marketplace_transaction(self, transaction_id: str, action: str) -> dict[str, Any]:
        if action not in {"authorize", "complete", "cancel"}:
            raise ValueError("action must be authorize, complete, or cancel")
        return self.request("POST", f"/v1/marketplace/transactions/{quote(transaction_id, safe='')}/{action}")

    def list_artifacts(self, task_id: str) -> list[dict[str, Any]]:
        return self.request("GET", f"/v1/tasks/{quote(task_id, safe='')}/artifacts")["artifacts"]

    def download_artifact(self, task_id: str, artifact_id: str, output: BinaryIO, offset: int = 0) -> int:
        headers = {"Authorization": f"Bearer {self.token}", "User-Agent": "agent-continuity-python/0.1.0"}
        if offset:
            headers["Range"] = f"bytes={offset}-"
        path = f"/v1/tasks/{quote(task_id, safe='')}/artifacts/{quote(artifact_id, safe='')}"
        request = Request(self.base_url + path, headers=headers, method="GET")
        with urlopen(request, timeout=self.timeout) as response:
            total = 0
            while chunk := response.read(1024 * 1024):
                output.write(chunk)
                total += len(chunk)
            return total
