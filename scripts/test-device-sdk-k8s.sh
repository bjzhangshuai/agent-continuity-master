#!/bin/sh
set -eu

PATH="${RANCHER_DESKTOP_BIN:-$HOME/.rd/bin}:$PATH"
export PATH
context=rancher-desktop
namespace=${CONTINUITY_NAMESPACE:-agent-continuity-dev}
release=${CONTINUITY_RELEASE:-agent-continuity}
repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
port=${CONTINUITY_DEVICE_SDK_PORT:-18082}

server=$(kubectl config view --context "$context" --minify -o jsonpath='{.clusters[0].cluster.server}')
case "$server" in https://127.0.0.1:*|https://localhost:*) ;; *) echo "refusing SDK E2E against non-local cluster $server" >&2; exit 1;; esac
node_names=$(kubectl --context "$context" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
case "$node_names" in *rancher-desktop*) ;; *) echo "refusing SDK E2E: target node is not Rancher Desktop" >&2; exit 1;; esac

bootstrap_token=$(kubectl --context "$context" -n "$namespace" get secret agent-continuity-secrets -o jsonpath='{.data.bootstrap-token}' | base64 -d)
temporary_dir=$(mktemp -d)
kubectl --context "$context" -n "$namespace" port-forward service/"$release" "$port":8080 >"$temporary_dir/port-forward.log" 2>&1 &
forward_pid=$!
cleanup() {
  kill "$forward_pid" >/dev/null 2>&1 || true
  wait "$forward_pid" >/dev/null 2>&1 || true
  rm -rf "$temporary_dir"
}
trap cleanup EXIT INT TERM

base_url="http://127.0.0.1:$port"
attempt=0
until curl --fail --silent "$base_url/v1/health/ready" >/dev/null; do
  attempt=$((attempt+1))
  if [ "$attempt" -ge 40 ]; then sed -n '1,120p' "$temporary_dir/port-forward.log" >&2; exit 1; fi
  sleep 0.25
done

account_json=$(curl --fail --silent --show-error -X POST "$base_url/v1/accounts" -H "X-Bootstrap-Token: $bootstrap_token" -H 'Content-Type: application/json' --data "{\"name\":\"Device SDK E2E $$\"}")
account_id=$(printf %s "$account_json" | jq -r .account_id)
token_json=$(curl --fail --silent --show-error -X POST "$base_url/v1/auth/token" -H "X-Bootstrap-Token: $bootstrap_token" -H 'Content-Type: application/json' --data "{\"account_id\":\"$account_id\",\"subject\":\"device-sdk-e2e\",\"ttl_seconds\":3600}")
access_token=$(printf %s "$token_json" | jq -r .access_token)

cd "$repository_dir"
make build >/dev/null
producer=$(CONTINUITY_E2E_URL="$base_url" CONTINUITY_E2E_TOKEN="$access_token" CONTINUITY_E2E_ACCOUNT_ID="$account_id" PYTHONPATH=sdk/python/src python3 tests/device-e2e/python_producer.py)
task_id=$(printf %s "$producer" | jq -r .task_id)
consumer=$(CONTINUITY_E2E_URL="$base_url" CONTINUITY_E2E_TOKEN="$access_token" CONTINUITY_E2E_ACCOUNT_ID="$account_id" CONTINUITY_E2E_TASK_ID="$task_id" node tests/device-e2e/typescript_consumer.mjs)

event_count=$(curl --fail --silent --show-error "$base_url/v1/tasks/$task_id/events?include_payload=true" -H "Authorization: Bearer $access_token" | jq '.events | length')
test "$event_count" -ge 2
mcp_output=$(printf '%s\n%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | AGENTCLOUD_API_URL="$base_url" AGENTCLOUD_TOKEN="$access_token" bin/agentcloud mcp stdio)
printf %s "$mcp_output" | grep -q 'continuity.get'

printf 'Python producer: %s\nTypeScript consumer: %s\nCross-device events: %s\nMCP stdio: passed\n' "$producer" "$consumer" "$event_count"
