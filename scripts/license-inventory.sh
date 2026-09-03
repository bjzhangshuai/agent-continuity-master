#!/bin/sh
set -eu

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repository_dir"

dependencies=$(go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}' ./... | sort -u)
missing=0
printf '"module","version","license_file"\n'
while IFS='|' read -r module version directory; do
  test -n "$module" || continue
  license=$(find "$directory" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'COPYING*' -o -iname 'NOTICE*' \) -print -quit)
  if test -z "$license"; then
    echo "dependency has no discoverable license file: $module $version" >&2
    missing=$((missing+1))
    continue
  fi
  printf '"%s","%s","%s"\n' "$module" "$version" "$(basename "$license")"
done <<EOF
$dependencies
EOF
test "$missing" -eq 0
