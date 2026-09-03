#!/bin/sh
set -eu

PATH="${RANCHER_DESKTOP_BIN:-$HOME/.rd/bin}:$PATH"
export PATH

context=rancher-desktop
namespace=${CONTINUITY_NAMESPACE:-agent-continuity-dev}
release=${CONTINUITY_RELEASE:-agent-continuity}
repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
chart="$repository_dir/deploy/helm/agent-continuity"
values="$repository_dir/deploy/environments/dev/values.yaml"
secret_name=agent-continuity-secrets

require_command() { command -v "$1" >/dev/null 2>&1 || { echo "required command not found: $1" >&2; exit 1; }; }
require_command kubectl
require_command helm
require_command docker
require_command openssl
require_command jq

# Only the explicitly chosen local demo operator can access merchant funds.
# Reuse the existing release setting; never grant treasury access to all payers.
treasury_owner=${CONTINUITY_TREASURY_OWNER_ACCOUNT_ID:-}
if [ -z "$treasury_owner" ]; then
  treasury_owner=$(helm get values "$release" --kube-context "$context" --namespace "$namespace" -o json 2>/dev/null | jq -r '.treasury.ownerAccountID // empty') || treasury_owner=
fi
treasury_demo=false
platform_admins=${CONTINUITY_PLATFORM_SUPER_ADMINS:-}
if [ -z "$platform_admins" ]; then
  platform_admins=$(helm get values "$release" --kube-context "$context" --namespace "$namespace" -o json 2>/dev/null | jq -r '.platform.superAdminAccountIDs // empty') || platform_admins=
fi
if [ -n "$treasury_owner" ]; then treasury_demo=true; fi

if ! kubectl config get-contexts "$context" >/dev/null 2>&1; then
  echo "refusing deployment: Kubernetes context $context does not exist" >&2
  exit 1
fi
server=$(kubectl config view --context "$context" --minify -o jsonpath='{.clusters[0].cluster.server}')
case "$server" in
  https://127.0.0.1:*|https://localhost:*) ;;
  *) echo "refusing deployment: $context points to non-local server $server" >&2; exit 1 ;;
esac
node_names=$(kubectl --context "$context" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
case "$node_names" in
  *rancher-desktop*) ;;
  *) echo "refusing deployment: node identity is not Rancher Desktop" >&2; exit 1 ;;
esac
if ! kubectl --context "$context" wait --for=condition=Ready node --all --timeout=30s; then
  echo "refusing deployment: local node is not healthy" >&2
  exit 1
fi

echo "Building a local image with Rancher Desktop's isolated Docker context..."
docker --context rancher-desktop build --tag agent-continuity:dev "$repository_dir"
image_id=$(docker --context rancher-desktop image inspect agent-continuity:dev --format '{{.Id}}')
case "$image_id" in
  sha256:*) ;;
  *) echo "refusing deployment: could not resolve immutable local image ID" >&2; exit 1 ;;
esac

kubectl --context "$context" create namespace "$namespace" --dry-run=client -o yaml | kubectl --context "$context" apply -f -
if ! kubectl --context "$context" -n "$namespace" get secret "$secret_name" >/dev/null 2>&1; then
  secret_dir=$(mktemp -d)
  trap 'rm -rf "$secret_dir"' EXIT INT TERM
  umask 077
  postgres_password=$(openssl rand -hex 24)
  jwt_secret=$(openssl rand -hex 32)
  bootstrap_token=$(openssl rand -hex 32)
  master_key=$(openssl rand -base64 32 | tr '/+' '_-' | tr -d '=')
  printf %s "$postgres_password" > "$secret_dir/postgres-password"
  printf %s "$jwt_secret" > "$secret_dir/jwt-secret"
  printf %s "$bootstrap_token" > "$secret_dir/bootstrap-token"
  printf %s "$master_key" > "$secret_dir/master-key"
  printf 'postgresql://continuity:%s@%s-postgres:5432/continuity?sslmode=disable' "$postgres_password" "$release" > "$secret_dir/database-url"
  kubectl --context "$context" -n "$namespace" create secret generic "$secret_name" \
    --from-file=postgres-password="$secret_dir/postgres-password" \
    --from-file=jwt-secret="$secret_dir/jwt-secret" \
    --from-file=bootstrap-token="$secret_dir/bootstrap-token" \
    --from-file=master-key="$secret_dir/master-key" \
    --from-file=database-url="$secret_dir/database-url"
  echo "Created namespace-scoped secret $secret_name. Existing secrets are always preserved."
fi

if [ -z "$(kubectl --context "$context" -n "$namespace" get secret "$secret_name" -o jsonpath='{.data.master-key}')" ]; then
  master_key=$(openssl rand -base64 32 | tr '/+' '_-' | tr -d '=')
  encoded_master_key=$(printf %s "$master_key" | base64 | tr -d '\n')
  kubectl --context "$context" -n "$namespace" patch secret "$secret_name" --type merge -p "{\"data\":{\"master-key\":\"$encoded_master_key\"}}" >/dev/null
  echo "Added a master-key field to the existing namespace-scoped secret without replacing its other values."
fi

helm upgrade --install "$release" "$chart" \
  --kube-context "$context" \
  --namespace "$namespace" \
  --values "$values" \
  --set-string image.buildID="$image_id" \
  --set-string treasury.ownerAccountID="$treasury_owner" \
  --set-string platform.superAdminAccountIDs="$platform_admins" \
  --set treasury.demoEnabled="$treasury_demo" \
  --rollback-on-failure --wait --timeout 5m

kubectl --context "$context" -n "$namespace" rollout status statefulset/"$release-postgres" --timeout=120s
kubectl --context "$context" -n "$namespace" rollout status deployment/"$release" --timeout=120s
kubectl --context "$context" wait --for=condition=Ready pod -l app.kubernetes.io/instance="$release" --namespace "$namespace" --timeout=120s
kubectl --context "$context" get nodes
kubectl --context "$context" -n "$namespace" get pods,services,pvc
echo "Deployment completed without changing the current kubectl context."
