// Package tenancy models platform authority separately from tenant membership.
package tenancy

import (
	"context"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"time"
)

var ErrConflict = errors.New("tenancy control version conflict")

type Grant struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	Role      string    `json:"role"`
	TenantID  string    `json:"tenant_id"` // "*" is platform-wide, never a data-access grant.
	ParentID  string    `json:"parent_id,omitempty"`
	GrantedBy string    `json:"granted_by"`
	Revoked   bool      `json:"revoked"`
	CreatedAt time.Time `json:"created_at"`
}
type Mandate struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	PayerTenantID string    `json:"payer_tenant_id"`
	Status        string    `json:"status"`
	RequestedBy   string    `json:"requested_by"`
	AcceptedBy    string    `json:"accepted_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}
type Event struct {
	ID         string    `json:"id"`
	ActorID    string    `json:"actor_id"`
	Action     string    `json:"action"`
	TenantID   string    `json:"tenant_id,omitempty"`
	ResourceID string    `json:"resource_id"`
	CreatedAt  time.Time `json:"created_at"`
}
type Control struct {
	Version  int64     `json:"version"`
	Grants   []Grant   `json:"grants"`
	Mandates []Mandate `json:"mandates"`
	Events   []Event   `json:"events"`
}
type Login struct{ Username, AccountID, DisplayName, PasswordHash string }
type Session struct {
	Hash, AccountID, Username      string
	CreatedAt, ExpiresAt, LastSeen time.Time
}
type Repository interface {
	ProvisionPortalUser(context.Context, *protocol.Account, Login, *protocol.OrganizationMember) error
	LoadTenancyControl(context.Context) (Control, error)
	SaveTenancyControl(context.Context, Control, int64) error
	AllOrganizations(context.Context) ([]protocol.Organization, error)
	CreatePortalLogin(context.Context, Login) error
	GetPortalLogin(context.Context, string) (Login, error)
	CreatePortalSession(context.Context, Session) error
	GetPortalSession(context.Context, string) (Session, error)
	DeletePortalSession(context.Context, string) error
	GetBillByID(context.Context, string) (protocol.BillingDocument, error)
	ListTenantBills(context.Context, string, int) ([]protocol.BillingDocument, error)
	GetTenantBillingAccount(context.Context, string) (protocol.BillingAccount, error)
}

const MaxDepth = 8

// Contains is a management-scope check, NOT a tenant content permission check.
func Contains(orgs []protocol.Organization, root, id string) bool {
	if root == "" || id == "" {
		return false
	}
	if root == "*" {
		return id != ""
	}
	for i := 0; i < MaxDepth; i++ {
		if id == root {
			return true
		}
		if id == "" || id == "*" {
			return false
		}
		next := ""
		for _, o := range orgs {
			if o.ID == id {
				next = o.ParentID
				break
			}
		}
		id = next
	}
	return false
}
func (c Control) ActiveGrant(id string) bool {
	seen := map[string]bool{}
	for depth := 0; depth < MaxDepth; depth++ {
		if id == "" {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		found := false
		for _, g := range c.Grants {
			if g.ID == id {
				if g.Revoked {
					return false
				}
				id = g.ParentID
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return id == ""
}
func ValidRole(role string) bool {
	switch role {
	case "admin", "operations", "support", "finance", "auditor":
		return true
	}
	return false
}
func (c Control) Allows(orgs []protocol.Organization, account, tenant, action string) bool {
	for _, g := range c.Grants {
		if g.AccountID != account || !c.ActiveGrant(g.ID) || !Contains(orgs, g.TenantID, tenant) {
			continue
		}
		if action == "read" || g.Role == "admin" || (action == "operate" && g.Role == "operations") || (action == "finance" && g.Role == "finance") {
			return true
		}
	}
	return false
}
