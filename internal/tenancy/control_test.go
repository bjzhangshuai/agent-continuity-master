package tenancy

import (
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"testing"
)

func TestScopesAndGrantChain(t *testing.T) {
	orgs := []protocol.Organization{{ID: "a"}, {ID: "b", ParentID: "a"}, {ID: "c"}}
	if Contains(orgs, "", "b") || Contains(orgs, "b", "a") || Contains(orgs, "a", "c") || !Contains(orgs, "a", "b") {
		t.Fatal("scope containment")
	}
	c := Control{Grants: []Grant{{ID: "root", AccountID: "hq", Role: "admin", TenantID: "a"}, {ID: "child", ParentID: "root", AccountID: "ops", Role: "operations", TenantID: "b"}}}
	if !c.Allows(orgs, "ops", "b", "operate") || c.Allows(orgs, "ops", "a", "operate") || c.Allows(orgs, "ops", "b", "delegate") || c.Allows(orgs, "ops", "b", "finance") {
		t.Fatal("role escalation")
	}
	c.Grants[0].Revoked = true
	if c.ActiveGrant("child") {
		t.Fatal("cascade revoke failed")
	}
	c.Grants[0].Revoked = false
	c.Grants[0].ParentID = "child"
	if c.ActiveGrant("child") {
		t.Fatal("cycle accepted")
	}
}
