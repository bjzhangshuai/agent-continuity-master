package archive

import (
	"strings"
	"testing"
)

func TestBrokerConfigurationSafety(t *testing.T) {
	c := BrokerConfig{URL: "tls://nats.example:4222", Stream: "ARCHIVE", Subject: "archive.jobs", Consumer: "workers", Replicas: 3, MaxBytes: 1 << 20, MaxMessages: 100}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Username = "eventbus-user"
	c.PasswordFile = "/run/nats/password"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.TokenFile = "/run/nats/token"
	if err := c.Validate(); err == nil {
		t.Fatal("ambiguous authentication accepted")
	}
	c.TokenFile = ""
	c.PasswordFile = ""
	if err := c.Validate(); err == nil {
		t.Fatal("unpaired basic credentials accepted")
	}
	c.Username = ""
	c.URL = "nats://nats.example:4222"
	if err := c.Validate(); err == nil {
		t.Fatal("unencrypted production transport accepted")
	}
	c.AllowInsecure = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.URL = "nats://user:password@nats.example:4222"
	if err := c.Validate(); err == nil {
		t.Fatal("credentials in URL accepted")
	}
}
func TestArchiveConfigRejectsUnknownAndMissingTargets(t *testing.T) {
	if _, err := Parse(`{"enabld":true}`); err == nil {
		t.Fatal("unknown field ignored")
	}
	if _, err := Parse(`{"enabled":true}`); err == nil {
		t.Fatal("incomplete enabled config accepted")
	}
	c, err := Parse("")
	if err != nil || c.Enabled || c.MaxPendingJobs == 0 {
		t.Fatalf("disabled defaults: %+v %v", c, err)
	}
	if _, err := Parse(`{"enabled":true,"broker":{"url":"nats://127.0.0.1:4222","allow_insecure":true},"targets":[]}`); err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("missing target not detected: %v", err)
	}
}
