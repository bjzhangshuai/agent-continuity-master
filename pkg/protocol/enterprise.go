package protocol

import "time"

// ExternalIdentity binds an identity-provider subject to one durable account.
// Provider is namespaced (for example oidc:https://issuer or scim:org_id) so
// subjects from different trust domains can never collide.
type ExternalIdentity struct {
	ID        string    `json:"external_identity_id"`
	Provider  string    `json:"provider"`
	Subject   string    `json:"subject"`
	AccountID string    `json:"account_id"`
	Email     string    `json:"email,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
