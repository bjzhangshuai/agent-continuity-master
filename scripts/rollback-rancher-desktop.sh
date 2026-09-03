#!/bin/sh
set -eu
PATH="${RANCHER_DESKTOP_BIN:-$HOME/.rd/bin}:$PATH"
export PATH
context=rancher-desktop
namespace=${CONTINUITY_NAMESPACE:-agent-continuity-dev}
release=${CONTINUITY_RELEASE:-agent-continuity}
server=$(kubectl config view --context "$context" --minify -o jsonpath='{.clusters[0].cluster.server}')
case "$server" in https://127.0.0.1:*|https://localhost:*) ;; *) echo "refusing rollback against non-local cluster $server" >&2; exit 1;; esac
helm rollback "$release" --kube-context "$context" --namespace "$namespace" --wait --timeout 3m
