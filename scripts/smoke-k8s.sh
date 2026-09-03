#!/bin/sh
set -eu

PATH="${RANCHER_DESKTOP_BIN:-$HOME/.rd/bin}:$PATH"
export PATH

context=rancher-desktop
namespace=${CONTINUITY_NAMESPACE:-agent-continuity-dev}
release=${CONTINUITY_RELEASE:-agent-continuity}
repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
port=${CONTINUITY_SMOKE_PORT:-18081}

server=$(kubectl config view --context "$context" --minify -o jsonpath='{.clusters[0].cluster.server}')
case "$server" in https://127.0.0.1:*|https://localhost:*) ;; *) echo "refusing smoke test against non-local cluster $server" >&2; exit 1;; esac
bootstrap_token=$(kubectl --context "$context" -n "$namespace" get secret agent-continuity-secrets -o jsonpath='{.data.bootstrap-token}' | base64 -d)
kubectl --context "$context" -n "$namespace" port-forward service/"$release" "$port":8080 >/tmp/agent-continuity-port-forward.log 2>&1 &
forward_pid=$!
cleanup() { kill "$forward_pid" >/dev/null 2>&1 || true; wait "$forward_pid" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

attempt=0
until curl --fail --silent "http://127.0.0.1:$port/v1/health/ready" >/dev/null; do
  attempt=$((attempt+1))
  if [ "$attempt" -ge 40 ]; then cat /tmp/agent-continuity-port-forward.log >&2; exit 1; fi
  sleep 0.25
done

cd "$repository_dir"
CONTINUITY_E2E_URL="http://127.0.0.1:$port" CONTINUITY_E2E_BOOTSTRAP_TOKEN="$bootstrap_token" go test -count=1 -v ./tests/e2e
