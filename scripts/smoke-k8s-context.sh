#!/bin/sh
set -eu

: "${CONTINUITY_KUBE_CONTEXT:?CONTINUITY_KUBE_CONTEXT is required}"
: "${CONTINUITY_NAMESPACE:?CONTINUITY_NAMESPACE is required}"
: "${CONTINUITY_RELEASE:?CONTINUITY_RELEASE is required}"

if [ "${CONTINUITY_ALLOW_NONLOCAL_K8S:-false}" != "true" ]; then
  echo "refusing non-local smoke without CONTINUITY_ALLOW_NONLOCAL_K8S=true" >&2
  exit 1
fi

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
port=${CONTINUITY_SMOKE_PORT:-18082}
bootstrap_token=$(kubectl --context "$CONTINUITY_KUBE_CONTEXT" -n "$CONTINUITY_NAMESPACE" get secret agent-continuity-secrets -o jsonpath='{.data.bootstrap-token}' | base64 -d)

kubectl --context "$CONTINUITY_KUBE_CONTEXT" -n "$CONTINUITY_NAMESPACE" port-forward service/"$CONTINUITY_RELEASE" "$port":8080 >"${TMPDIR:-/tmp}/agent-continuity-cd-port-forward.log" 2>&1 &
forward_pid=$!
cleanup() { kill "$forward_pid" >/dev/null 2>&1 || true; wait "$forward_pid" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

attempt=0
until curl --fail --silent "http://127.0.0.1:$port/v1/health/ready" >/dev/null; do
  attempt=$((attempt+1))
  if [ "$attempt" -ge 60 ]; then
    cat "${TMPDIR:-/tmp}/agent-continuity-cd-port-forward.log" >&2
    exit 1
  fi
  sleep 0.5
done

cd "$repository_dir"
CONTINUITY_E2E_URL="http://127.0.0.1:$port" CONTINUITY_E2E_BOOTSTRAP_TOKEN="$bootstrap_token" go test -count=1 -v ./tests/e2e
