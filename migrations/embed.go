package migrations

import "embed"

// Files contains versioned forward migrations used by the migration command.
//
//go:embed *.up.sql
var Files embed.FS
