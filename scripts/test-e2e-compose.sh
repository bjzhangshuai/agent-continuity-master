#!/bin/sh
set -eu

PATH="${RANCHER_DESKTOP_BIN:-$HOME/.rd/bin}:$PATH"
export PATH

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
compose_file="$repository_dir/deploy/compose/docker-compose.yaml"
project_name="agent-continuity-e2e-$$"
docker_context=${CONTINUITY_DOCKER_CONTEXT:-rancher-desktop}
postgres_password=$(openssl rand -hex 24)
jwt_secret=$(openssl rand -hex 32)
bootstrap_token=$(openssl rand -hex 32)
master_key=$(openssl rand -base64 32 | tr '/+' '_-' | tr -d '=')
minio_root_user="continuity$(openssl rand -hex 6)"
minio_root_password=$(openssl rand -base64 32 | tr -d '\n')
port=${CONTINUITY_E2E_PORT:-18080}

cleanup() {
  POSTGRES_PASSWORD="$postgres_password" CONTINUITY_JWT_SECRET="$jwt_secret" CONTINUITY_BOOTSTRAP_TOKEN="$bootstrap_token" CONTINUITY_MASTER_KEY="$master_key" MINIO_ROOT_USER="$minio_root_user" MINIO_ROOT_PASSWORD="$minio_root_password" CONTINUITY_PORT="$port" \
    docker --context "$docker_context" compose -p "$project_name" -f "$compose_file" down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

POSTGRES_PASSWORD="$postgres_password" CONTINUITY_JWT_SECRET="$jwt_secret" CONTINUITY_BOOTSTRAP_TOKEN="$bootstrap_token" CONTINUITY_MASTER_KEY="$master_key" MINIO_ROOT_USER="$minio_root_user" MINIO_ROOT_PASSWORD="$minio_root_password" CONTINUITY_PORT="$port" \
  docker --context "$docker_context" compose -p "$project_name" -f "$compose_file" up --build --detach --wait postgres minio server

attempt=0
until curl --fail --silent "http://127.0.0.1:$port/v1/health/ready" >/dev/null; do
  attempt=$((attempt+1))
  if [ "$attempt" -ge 40 ]; then
    POSTGRES_PASSWORD="$postgres_password" CONTINUITY_JWT_SECRET="$jwt_secret" CONTINUITY_BOOTSTRAP_TOKEN="$bootstrap_token" CONTINUITY_MASTER_KEY="$master_key" MINIO_ROOT_USER="$minio_root_user" MINIO_ROOT_PASSWORD="$minio_root_password" CONTINUITY_PORT="$port" \
      docker --context "$docker_context" compose -p "$project_name" -f "$compose_file" ps >&2
    exit 1
  fi
  sleep 0.25
done

# Build the matching upstream client and exercise S3 against this test-only store.
docker --context "$docker_context" build --target mc --file "$repository_dir/deploy/images/objectstore/Dockerfile" --tag agent-continuity-mc:source-77f82e18b540 "$repository_dir"
MINIO_ROOT_USER="$minio_root_user" MINIO_ROOT_PASSWORD="$minio_root_password" \
  docker --context "$docker_context" run --rm --network "${project_name}_continuity" \
    --env MINIO_ROOT_USER --env MINIO_ROOT_PASSWORD --entrypoint /bin/sh \
    agent-continuity-mc:source-77f82e18b540 -ec '
      mc alias set probe http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" --api S3v4 >/dev/null
      mc mb probe/continuity-e2e-probe >/dev/null
      printf "%s" "continuity-s3-roundtrip" | mc pipe probe/continuity-e2e-probe/probe.txt >/dev/null
      test "$(mc cat probe/continuity-e2e-probe/probe.txt)" = "continuity-s3-roundtrip"
      echo "PASS: source-built MinIO and mc S3 upload/download"
    '

CONTINUITY_E2E_URL="http://127.0.0.1:$port" CONTINUITY_E2E_BOOTSTRAP_TOKEN="$bootstrap_token" \
  go test -count=1 -v ./tests/e2e
