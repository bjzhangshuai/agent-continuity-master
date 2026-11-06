#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
extension="$root/integrations/codebuddy-extension"
cleanup() {
  rm -rf "$extension/sdk" "$extension/bridge.py" "$extension/LICENSE"
}
trap cleanup EXIT INT TERM
cp "$root/integrations/codebuddy/bridge.py" "$extension/bridge.py"
cp "$root/LICENSE" "$extension/LICENSE"
mkdir -p "$extension/sdk/agent_continuity"
cp "$root/sdk/python/src/agent_continuity/"*.py "$extension/sdk/agent_continuity/"
cd "$extension"
npx --yes @vscode/vsce package --no-dependencies --out agent-continuity-codebuddy.vsix
