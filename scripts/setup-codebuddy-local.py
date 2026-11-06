#!/usr/bin/env python3
"""Create a least-privilege local CodeBuddy experiment (secrets never printed)."""
import argparse
import json
import os
from pathlib import Path
import stat
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "sdk/python/src"))
from agent_continuity import Client, generate_device_identity


def atomic(path, value, mode):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("x") as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.flush(); os.fsync(stream.fileno())
    temporary.chmod(mode); temporary.replace(path)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--api-url", default="http://127.0.0.1:18080")
    parser.add_argument("--account-id", required=True)
    parser.add_argument("--organization-id", default="")
    parser.add_argument("--bootstrap-env", default="AGENTCLOUD_BOOTSTRAP_TOKEN")
    parser.add_argument("--transcript-root", action="append", required=True)
    parser.add_argument("--device-name", default="MacBook · CodeBuddy 本机")
    parser.add_argument("--profile", default="device-a", choices=("device-a", "device-b"))
    args = parser.parse_args()
    workspace = Path(args.workspace).resolve(strict=True)
    state = ROOT / ".data/codebuddy-local" / args.profile
    state.mkdir(parents=True, exist_ok=True, mode=0o700); state.chmod(0o700)
    credential_path = state / "credential.json"
    credential = json.loads(credential_path.read_text()) if credential_path.exists() else {}
    scopes = ["tasks:read", "tasks:write", "tasks:admin", "devices:read", "devices:write",
              "agents:read", "agents:write", "usage:read", "billing:read", "billing:write"]
    limited = None
    if credential.get("api_url") == args.api_url and credential.get("account_id") == args.account_id and credential.get("expires_at_epoch", 0) > time.time() + 300:
        candidate = Client(args.api_url, credential["token"], timeout=10)
        try:
            candidate.health(); candidate.list_agents(); limited = candidate
        except Exception:
            pass
    if limited is None:
        bootstrap = os.environ.get(args.bootstrap_env, "")
        if not bootstrap: raise RuntimeError(args.bootstrap_env + " is required for first setup or credential renewal")
        api = Client(args.api_url, timeout=10)
        issued = api.issue_token(bootstrap, args.account_id, "codebuddy-local-setup")
        api.token = issued["access_token"]
        key = api.request("POST", "/v1/api-keys", {"name": "CodeBuddy 本地实验 · " + args.profile, "scopes": scopes, "ttl_seconds": 86400})
        credential = {"api_url": args.api_url, "account_id": args.account_id,
                      "token": key["secret"], "api_key_id": key["api_key"]["api_key_id"], "expires_at_epoch": int(time.time()) + 86400}
        atomic(credential_path, credential, 0o600)
        limited = Client(args.api_url, credential["token"], timeout=10)
    identity_path = state / "identity.json"
    if identity_path.exists(): identity = json.loads(identity_path.read_text())
    else:
        identity = generate_device_identity(args.device_name).to_dict()
        atomic(identity_path, identity, 0o600)
    try:
        limited.register_device(identity)
    except Exception as error:
        if getattr(error, "status", 0) != 409:
            raise
        existing = next((d for d in limited.list_devices(True) if d["device_id"] == identity["device_id"]), None)
        if not existing or existing.get("public_key") != identity["signing_public_key"] or existing.get("encryption_public_key") != identity["encryption_public_key"]:
            raise RuntimeError("device ID exists with different public keys") from error
    agents = limited.list_agents()
    agent = next((a for a in agents if a["provider"] == "codebuddy" and a["name"] == "CodeBuddy CN · 本地插件"), None)
    if not agent:
        agent = limited.create_agent("CodeBuddy CN · 本地插件", "codebuddy", ["event_capture", "rehydrate", "artifacts"], model="CodeBuddy CN 4.10.3", card={"integration": "hooks+mcp+extension", "capture": "opt-in"})
    bridge = str(ROOT / "integrations/codebuddy/bridge.py")
    python = sys.executable
    command = f'"{python}" "{bridge}" --workspace "{workspace}" hook'
    config = {"api_url": args.api_url, "state_dir": str(state), "organization_id": args.organization_id,
              "agent_id": agent["agent_id"], "capture_consent": True,
              "transcript_roots": [str(Path(p).resolve(strict=True)) for p in args.transcript_root]}
    atomic(workspace / ".continuity/config.json", config, 0o600)
    settings = {"hooks": {event: [{"matcher": "*", "hooks": [{"type": "command", "command": command, "timeout": 20}]}]
               for event in ("SessionStart", "Stop", "SessionEnd", "PreCompact")}}
    atomic(workspace / ".codebuddy/settings.json", settings, 0o600)
    mcp_config = {"mcpServers": {"agent-continuity": {"type": "stdio", "command": python,
                  "args": [bridge, "--workspace", str(workspace), "mcp"]}}}
    atomic(workspace / ".codebuddy/mcp.json", mcp_config, 0o600)
    print(json.dumps({"workspace": str(workspace), "device_id": identity["device_id"], "agent_id": agent["agent_id"],
                      "capture_consent": True, "credential_mode": oct(stat.S_IMODE((state / "credential.json").stat().st_mode)),
                      "profile": args.profile, "api_key_expires_in_hours": round((credential["expires_at_epoch"] - time.time()) / 3600, 1),
                      "secrets_printed": False}, ensure_ascii=False))


if __name__ == "__main__":
    main()
