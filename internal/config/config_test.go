package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvOrFileSupportsMountedBYOKAndRejectsAmbiguity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master-key")
	if err := os.WriteFile(path, []byte("mounted-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SECRET", "")
	t.Setenv("TEST_SECRET_FILE", path)
	value, err := envOrFile("TEST_SECRET", "TEST_SECRET_FILE")
	if err != nil || value != "mounted-secret" {
		t.Fatalf("mounted secret: %q %v", value, err)
	}
	t.Setenv("TEST_SECRET", "inline-secret")
	if _, err := envOrFile("TEST_SECRET", "TEST_SECRET_FILE"); err == nil {
		t.Fatal("ambiguous secret sources were accepted")
	}
}

func TestValidateRejectsWeakSecrets(t *testing.T) {
	cfg := Config{ListenAddress: ":8080", Store: "memory", JWTSecret: "short", BootstrapToken: "short", MaxBodyBytes: 1 << 20}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected weak secret validation error")
	}
}

func TestValidateMemoryConfiguration(t *testing.T) {
	cfg := Config{ListenAddress: ":8080", Store: "memory", ArtifactBackend: "local", JWTSecret: "01234567890123456789012345678901", BootstrapToken: "012345678901234567890123", MasterKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("m", 32))), ServiceName: "agent-continuity-test", MaxBodyBytes: 1 << 20}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
