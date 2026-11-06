// Package api contains the public, versioned integration contract artifacts.
package api

import "embed"

//go:embed continuity-profile-v1.json schemas/*.json openapi.yaml
var contracts embed.FS

func Contract(name string) ([]byte, error) { return contracts.ReadFile(name) }
