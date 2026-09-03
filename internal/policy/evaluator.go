package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type Context struct {
	SubjectID    string
	Roles        []string
	Action       string
	ResourceType string
	Attributes   map[string]any
	Now          time.Time
}

type Decision struct {
	Allowed         bool     `json:"allowed"`
	MatchedPolicies []string `json:"matched_policies,omitempty"`
	Reason          string   `json:"reason"`
}

// Evaluate combines baseline RBAC with organization ABAC. Explicit deny always
// wins; otherwise an explicit allow or the baseline role grant is sufficient.
func Evaluate(value Context, policies []protocol.AccessPolicy) (Decision, error) {
	if value.SubjectID == "" || value.Action == "" || value.ResourceType == "" {
		return Decision{}, fmt.Errorf("subject, action and resource type are required")
	}
	if value.Now.IsZero() {
		value.Now = time.Now().UTC()
	}
	sort.SliceStable(policies, func(i, j int) bool { return policies[i].Priority > policies[j].Priority })
	baseline := roleAllows(value.Roles, value.Action)
	allowed := false
	matched := []string{}
	for _, rule := range policies {
		if rule.ResourceType != "*" && rule.ResourceType != value.ResourceType {
			continue
		}
		if !matchesAny(rule.Actions, value.Action) || !matchesSubject(rule.Subjects, value.SubjectID, value.Roles) {
			continue
		}
		matchedConditions, err := conditionsMatch(rule.Conditions, value)
		if err != nil {
			return Decision{}, fmt.Errorf("policy %s: %w", rule.ID, err)
		}
		if !matchedConditions {
			continue
		}
		matched = append(matched, rule.ID)
		if rule.Effect == "deny" {
			return Decision{Allowed: false, MatchedPolicies: matched, Reason: "explicit deny policy"}, nil
		}
		if rule.Effect == "allow" {
			allowed = true
		}
	}
	if allowed {
		return Decision{Allowed: true, MatchedPolicies: matched, Reason: "allow policy"}, nil
	}
	if baseline {
		return Decision{Allowed: true, MatchedPolicies: matched, Reason: "role grant"}, nil
	}
	return Decision{Allowed: false, MatchedPolicies: matched, Reason: "no matching grant"}, nil
}

func roleAllows(roles []string, action string) bool {
	grants := map[string][]string{
		"owner":   {"*"},
		"admin":   {"organization.*", "team.*", "task.*", "artifact.*", "member.*", "policy.*", "webhook.*", "developer.*", "billing.read"},
		"member":  {"organization.read", "member.read", "team.read", "task.read", "task.create", "task.write", "artifact.read", "artifact.write", "agent.read", "agent.use"},
		"viewer":  {"organization.read", "member.read", "team.read", "task.read", "artifact.read", "agent.read"},
		"billing": {"organization.read", "billing.*", "usage.read"},
	}
	for _, role := range roles {
		if matchesAny(grants[role], action) {
			return true
		}
	}
	return false
}

func matchesAny(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || pattern == value || strings.HasSuffix(pattern, ".*") && strings.HasPrefix(value, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

func matchesSubject(subjects []string, subjectID string, roles []string) bool {
	for _, subject := range subjects {
		if subject == "*" || subject == "account:"+subjectID {
			return true
		}
		if strings.HasPrefix(subject, "role:") {
			role := strings.TrimPrefix(subject, "role:")
			for _, actual := range roles {
				if actual == role {
					return true
				}
			}
		}
	}
	return false
}

func conditionsMatch(conditions []protocol.PolicyCondition, value Context) (bool, error) {
	for _, condition := range conditions {
		actual, exists := conditionValue(condition.Key, value)
		var expected any
		if len(condition.Value) > 0 && json.Unmarshal(condition.Value, &expected) != nil {
			return false, fmt.Errorf("condition %s has invalid value", condition.Key)
		}
		switch condition.Operator {
		case "exists":
			wanted, _ := expected.(bool)
			if exists != wanted {
				return false, nil
			}
		case "equals":
			if !exists || !reflect.DeepEqual(normalize(actual), normalize(expected)) {
				return false, nil
			}
		case "not_equals":
			if exists && reflect.DeepEqual(normalize(actual), normalize(expected)) {
				return false, nil
			}
		case "in":
			items, ok := expected.([]any)
			if !ok || !containsValue(items, actual) {
				return false, nil
			}
		case "contains":
			if !containsActual(actual, expected) {
				return false, nil
			}
		case "before", "after":
			expectedTime, err := parseTime(expected)
			if err != nil {
				return false, err
			}
			actualTime, err := parseTime(actual)
			if err != nil || condition.Operator == "before" && !actualTime.Before(expectedTime) || condition.Operator == "after" && !actualTime.After(expectedTime) {
				return false, nil
			}
		default:
			return false, fmt.Errorf("unsupported operator %q", condition.Operator)
		}
	}
	return true, nil
}

func conditionValue(key string, value Context) (any, bool) {
	switch key {
	case "subject.id":
		return value.SubjectID, true
	case "subject.roles":
		return value.Roles, true
	case "action":
		return value.Action, true
	case "resource.type":
		return value.ResourceType, true
	case "time.now":
		return value.Now.UTC().Format(time.RFC3339Nano), true
	}
	if strings.HasPrefix(key, "resource.") {
		actual, ok := value.Attributes[strings.TrimPrefix(key, "resource.")]
		return actual, ok
	}
	return nil, false
}

func normalize(value any) any {
	switch typed := value.(type) {
	case float64:
		return fmt.Sprint(typed)
	case int, int64, uint64:
		return fmt.Sprint(typed)
	default:
		return value
	}
}

func containsValue(values []any, actual any) bool {
	for _, value := range values {
		if reflect.DeepEqual(normalize(value), normalize(actual)) {
			return true
		}
	}
	return false
}

func containsActual(actual, expected any) bool {
	switch typed := actual.(type) {
	case []string:
		for _, value := range typed {
			if reflect.DeepEqual(normalize(value), normalize(expected)) {
				return true
			}
		}
	case []any:
		return containsValue(typed, expected)
	case string:
		expectedString, ok := expected.(string)
		return ok && strings.Contains(typed, expectedString)
	}
	return false
}

func parseTime(value any) (time.Time, error) {
	text, ok := value.(string)
	if !ok {
		return time.Time{}, errors.New("time condition requires RFC3339 string")
	}
	return time.Parse(time.RFC3339Nano, text)
}
