#!/bin/sh
# Builds only two explicitly named images. Does not start containers or deploy.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
context=${CONTINUITY_DOCKER_CONTEXT:-rancher-desktop}
PATH="${RANCHER_DESKTOP_BIN:-$HOME/.rd/bin}:$PATH"
export PATH
docker --context "$context" build --target minio --file "$root/deploy/images/objectstore/Dockerfile" --tag agent-continuity-minio:source-7aac2a2c5b7c "$root"
docker --context "$context" build --target mc --file "$root/deploy/images/objectstore/Dockerfile" --tag agent-continuity-mc:source-77f82e18b540 "$root"
