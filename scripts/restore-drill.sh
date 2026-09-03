#!/bin/sh
set -eu

: "${CONTINUITY_RESTORE_BUNDLE:?CONTINUITY_RESTORE_BUNDLE is required}"
: "${CONTINUITY_RESTORE_DATABASE_URL:?CONTINUITY_RESTORE_DATABASE_URL is required}"
: "${CONTINUITY_RESTORE_ARTIFACT_DIR:?CONTINUITY_RESTORE_ARTIFACT_DIR is required}"

case "$CONTINUITY_RESTORE_DATABASE_URL" in
  *restore_drill*|*restore-drill*) ;;
  *) echo "refusing restore: target database URL must contain restore_drill" >&2; exit 1 ;;
esac
case "$CONTINUITY_RESTORE_ARTIFACT_DIR" in
  *restore-drill*|*restore_drill*) ;;
  *) echo "refusing restore: artifact target must contain restore-drill" >&2; exit 1 ;;
esac
if [ ! -f "$CONTINUITY_RESTORE_BUNDLE/postgres.dump" ] || [ ! -f "$CONTINUITY_RESTORE_BUNDLE/artifacts.tar.gz" ]; then
  echo "backup bundle is incomplete" >&2
  exit 1
fi
if [ -e "$CONTINUITY_RESTORE_ARTIFACT_DIR" ] && [ "$(find "$CONTINUITY_RESTORE_ARTIFACT_DIR" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
  echo "restore artifact target must be empty" >&2
  exit 1
fi

(cd "$CONTINUITY_RESTORE_BUNDLE" && if command -v sha256sum >/dev/null 2>&1; then sha256sum -c SHA256SUMS; else shasum -a 256 -c SHA256SUMS; fi)
pg_restore --dbname="$CONTINUITY_RESTORE_DATABASE_URL" --no-owner --no-privileges --exit-on-error "$CONTINUITY_RESTORE_BUNDLE/postgres.dump"
mkdir -p "$CONTINUITY_RESTORE_ARTIFACT_DIR"
tar -C "$CONTINUITY_RESTORE_ARTIFACT_DIR" -xzf "$CONTINUITY_RESTORE_BUNDLE/artifacts.tar.gz"
psql "$CONTINUITY_RESTORE_DATABASE_URL" -v ON_ERROR_STOP=1 -Atc "SELECT COUNT(*) FROM schema_migrations; SELECT COUNT(*) FROM tasks; SELECT COUNT(*) FROM events;"
echo "restore drill completed in isolated targets"
