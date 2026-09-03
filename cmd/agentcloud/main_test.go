package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestParseShareOptions(t *testing.T) {
	permission, path, err := parseShareOptions([]string{"write", "--qr", "invite.svg"})
	if err != nil {
		t.Fatal(err)
	}
	if permission != protocol.PermissionWrite || path != "invite.svg" {
		t.Fatalf("unexpected share options: %q %q", permission, path)
	}
	permission, path, err = parseShareOptions(nil)
	if err != nil || permission != protocol.PermissionRead || path != "" {
		t.Fatalf("unexpected defaults: %q %q %v", permission, path, err)
	}
	if _, _, err := parseShareOptions([]string{"admin"}); err == nil {
		t.Fatal("expected unsupported permission to fail")
	}
	if _, _, err := parseShareOptions([]string{"--qr"}); err == nil {
		t.Fatal("expected missing QR output path to fail")
	}
}

func TestCanonicalAdapterSourceResolvesDirectorySymlinks(t *testing.T) {
	realRoot := t.TempDir()
	logPath := filepath.Join(realRoot, "session.jsonl")
	if err := os.WriteFile(logPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(t.TempDir(), "linked-logs")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	throughLink, err := canonicalAdapterSource(filepath.Join(linkRoot, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	direct, err := canonicalAdapterSource(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if throughLink != direct {
		t.Fatalf("canonical paths differ: %q != %q", throughLink, direct)
	}
}
