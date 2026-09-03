#!/bin/sh
set -eu

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tool_dir=$(mktemp -d "${TMPDIR:-/tmp}/agent-continuity-gosec.XXXXXX")
cleanup() {
  case "$tool_dir" in
    *agent-continuity-gosec.*) find "$tool_dir" -depth -delete 2>/dev/null || true ;;
  esac
}
trap cleanup EXIT INT TERM

GOBIN="$tool_dir" go install github.com/securego/gosec/v2/cmd/gosec@v2.22.8
cd "$repository_dir"
for package in $(go list ./...); do
  echo "gosec $package"
  "$tool_dir/gosec" -quiet "$package"
done
