#!/bin/sh
set -eu

: "${CONTINUITY_BACKUP_DATABASE_URL:?CONTINUITY_BACKUP_DATABASE_URL is required}"
: "${CONTINUITY_BACKUP_DIR:?CONTINUITY_BACKUP_DIR is required}"
: "${CONTINUITY_ARTIFACT_DIR:?CONTINUITY_ARTIFACT_DIR is required for local-artifact backup}"

case "$CONTINUITY_BACKUP_DIR" in
  /|"$HOME"|"") echo "refusing unsafe backup directory" >&2; exit 1 ;;
esac
case "$CONTINUITY_ARTIFACT_DIR" in
  /|"$HOME"|"") echo "refusing unsafe artifact directory" >&2; exit 1 ;;
esac
command -v pg_dump >/dev/null
command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p "$CONTINUITY_BACKUP_DIR"
temporary=$(mktemp -d "$CONTINUITY_BACKUP_DIR/.backup-$timestamp-XXXXXX")
cleanup() { rm -rf "$temporary"; }
trap cleanup EXIT INT TERM

pg_dump --dbname="$CONTINUITY_BACKUP_DATABASE_URL" --format=custom --no-owner --no-privileges --file="$temporary/postgres.dump"
tar -C "$CONTINUITY_ARTIFACT_DIR" -czf "$temporary/artifacts.tar.gz" .
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$temporary" && sha256sum postgres.dump artifacts.tar.gz > SHA256SUMS)
else
  (cd "$temporary" && shasum -a 256 postgres.dump artifacts.tar.gz > SHA256SUMS)
fi
schema_versions=$(psql "$CONTINUITY_BACKUP_DATABASE_URL" -Atc "SELECT string_agg(version, ',' ORDER BY version) FROM schema_migrations" 2>/dev/null || true)
cat > "$temporary/manifest.json" <<EOF
{"format_version":1,"created_at":"$timestamp","schema_versions":"$schema_versions","artifact_backend":"local","key_material_included":false}
EOF
destination="$CONTINUITY_BACKUP_DIR/agent-continuity-$timestamp"
mv "$temporary" "$destination"
trap - EXIT INT TERM
echo "$destination"
