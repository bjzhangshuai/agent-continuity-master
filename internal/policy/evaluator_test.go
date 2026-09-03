package policy

import (
	"encoding/json"
	"testing"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestExplicitDenyOverridesRoleAndAllow(t *testing.T) {
	policies := []protocol.AccessPolicy{
		{ID: "allow", Effect: "allow", Subjects: []string{"role:member"}, Actions: []string{"task.write"}, ResourceType: "task"},
		{ID: "deny", Effect: "deny", Subjects: []string{"*"}, Actions: []string{"task.write"}, ResourceType: "task", Priority: 100, Conditions: []protocol.PolicyCondition{{Key: "resource.classification", Operator: "equals", Value: json.RawMessage(`"restricted"`)}}},
	}
	decision, err := Evaluate(Context{SubjectID: "acct", Roles: []string{"member"}, Action: "task.write", ResourceType: "task", Attributes: map[string]any{"classification": "restricted"}}, policies)
	if err != nil || decision.Allowed || decision.Reason != "explicit deny policy" {
		t.Fatalf("deny did not win: %+v err=%v", decision, err)
	}
}

func TestRBACAndABACConditions(t *testing.T) {
	decision, err := Evaluate(Context{SubjectID: "acct", Roles: []string{"viewer"}, Action: "task.read", ResourceType: "task"}, nil)
	if err != nil || !decision.Allowed {
		t.Fatalf("viewer read denied: %+v err=%v", decision, err)
	}
	policy := protocol.AccessPolicy{ID: "region", Effect: "allow", Subjects: []string{"account:acct"}, Actions: []string{"artifact.write"}, ResourceType: "artifact", Conditions: []protocol.PolicyCondition{{Key: "resource.region", Operator: "in", Value: json.RawMessage(`["cn","sg"]`)}}}
	decision, err = Evaluate(Context{SubjectID: "acct", Action: "artifact.write", ResourceType: "artifact", Attributes: map[string]any{"region": "cn"}}, []protocol.AccessPolicy{policy})
	if err != nil || !decision.Allowed {
		t.Fatalf("ABAC allow failed: %+v err=%v", decision, err)
	}
}
