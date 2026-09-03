package auth

import (
	"strings"
	"testing"
	"time"
)

func TestIssueAndParse(t *testing.T) {
	m, err := NewManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return time.Unix(1000, 0) }
	token, err := m.Issue("acct_1", "device_1", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := m.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.AccountID != "acct_1" || claims.Subject != "device_1" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestRejectTamperedToken(t *testing.T) {
	m, _ := NewManager(strings.Repeat("s", 32))
	token, _ := m.Issue("acct_1", "user", time.Minute)
	token = token[:len(token)-1] + "x"
	if _, err := m.Parse(token); err == nil {
		t.Fatal("expected signature error")
	}
}
