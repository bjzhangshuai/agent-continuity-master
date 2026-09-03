package protocol

import (
	"encoding/json"
	"time"
)

type Organization struct {
	ParentID       string          `json:"parent_id,omitempty"`
	ID             string          `json:"organization_id"`
	Name           string          `json:"name"`
	Slug           string          `json:"slug"`
	OwnerAccountID string          `json:"owner_account_id"`
	Plan           string          `json:"plan"`
	DataResidency  string          `json:"data_residency,omitempty"`
	KeyProvider    string          `json:"key_provider"`
	Settings       json.RawMessage `json:"settings,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type OrganizationMember struct {
	OrganizationID string     `json:"organization_id"`
	AccountID      string     `json:"account_id"`
	Role           string     `json:"role"`
	Status         string     `json:"status"`
	JoinedAt       time.Time  `json:"joined_at"`
	RemovedAt      *time.Time `json:"removed_at,omitempty"`
}

type Team struct {
	ID             string          `json:"team_id"`
	OrganizationID string          `json:"organization_id"`
	Name           string          `json:"name"`
	Slug           string          `json:"slug"`
	Settings       json.RawMessage `json:"settings,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

type TeamMember struct {
	TeamID    string     `json:"team_id"`
	AccountID string     `json:"account_id"`
	Role      string     `json:"role"`
	Status    string     `json:"status"`
	JoinedAt  time.Time  `json:"joined_at"`
	RemovedAt *time.Time `json:"removed_at,omitempty"`
}

type PolicyCondition struct {
	Key      string          `json:"key"`
	Operator string          `json:"operator"`
	Value    json.RawMessage `json:"value"`
}

type AccessPolicy struct {
	ID             string            `json:"policy_id"`
	OrganizationID string            `json:"organization_id"`
	Name           string            `json:"name"`
	Effect         string            `json:"effect"`
	Subjects       []string          `json:"subjects"`
	Actions        []string          `json:"actions"`
	ResourceType   string            `json:"resource_type"`
	Conditions     []PolicyCondition `json:"conditions,omitempty"`
	Priority       int               `json:"priority"`
	CreatedBy      string            `json:"created_by"`
	CreatedAt      time.Time         `json:"created_at"`
}

type ArtifactPermission struct {
	TaskID     string     `json:"task_id"`
	ArtifactID string     `json:"artifact_id"`
	AccountID  string     `json:"account_id"`
	Permission Permission `json:"permission"`
	GrantedBy  string     `json:"granted_by"`
	CreatedAt  time.Time  `json:"created_at"`
}

type QRInvitation struct {
	Version              uint32          `json:"version"`
	InvitationID         string          `json:"invitation_id"`
	TaskID               string          `json:"task_id"`
	InviterAccountID     string          `json:"inviter_identity"`
	InviterDeviceID      string          `json:"inviter_device_id"`
	Permission           Permission      `json:"permission_scope"`
	ExpiresAt            time.Time       `json:"expiration"`
	Nonce                string          `json:"nonce"`
	TokenDigest          string          `json:"token_digest"`
	KeyExchangeReference json.RawMessage `json:"key_exchange_material,omitempty"`
	Signature            string          `json:"signature"`
	Token                string          `json:"token,omitempty"`
}

type QRInvitationRender struct {
	Invitation QRInvitation `json:"invitation"`
	Content    string       `json:"qr_content"`
	SVG        string       `json:"qr_svg"`
}

type OAuthClient struct {
	ID           string     `json:"client_id"`
	AccountID    string     `json:"account_id"`
	Name         string     `json:"name"`
	SecretHash   string     `json:"-"`
	Scopes       []string   `json:"scopes"`
	RedirectURIs []string   `json:"redirect_uris,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

type WebhookEndpoint struct {
	ID         string     `json:"webhook_id"`
	AccountID  string     `json:"account_id"`
	URL        string     `json:"url"`
	Events     []string   `json:"events"`
	SecretHash string     `json:"-"`
	Secret     string     `json:"secret,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	DisabledAt *time.Time `json:"disabled_at,omitempty"`
}

type WebhookDelivery struct {
	ID             string          `json:"delivery_id"`
	WebhookID      string          `json:"webhook_id"`
	EventType      string          `json:"event_type"`
	Payload        json.RawMessage `json:"payload"`
	Attempts       int             `json:"attempts"`
	NextAttempt    time.Time       `json:"next_attempt"`
	LastStatus     int             `json:"last_status,omitempty"`
	LastError      string          `json:"last_error,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	DeliveredAt    *time.Time      `json:"delivered_at,omitempty"`
	DeadLetteredAt *time.Time      `json:"dead_lettered_at,omitempty"`
}

type UsageRecord struct {
	ID             string          `json:"usage_id"`
	AccountID      string          `json:"account_id"`
	OrganizationID string          `json:"organization_id,omitempty"`
	Metric         string          `json:"metric"`
	Quantity       int64           `json:"quantity"`
	Unit           string          `json:"unit"`
	ResourceID     string          `json:"resource_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	RecordedAt     time.Time       `json:"recorded_at"`
}

type BillingAccount struct {
	PayerOrganizationID string          `json:"payer_organization_id,omitempty"`
	PaymentMandateID    string          `json:"payment_mandate_id,omitempty"`
	ID                  string          `json:"billing_account_id"`
	AccountID           string          `json:"account_id"`
	OrganizationID      string          `json:"organization_id,omitempty"`
	Plan                string          `json:"plan"`
	Status              string          `json:"status"`
	Currency            string          `json:"currency"`
	ProviderCustomerRef string          `json:"provider_customer_reference,omitempty"`
	Limits              json.RawMessage `json:"limits"`
	CurrentPeriodStart  time.Time       `json:"current_period_start"`
	CurrentPeriodEnd    time.Time       `json:"current_period_end"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type MarketplaceListing struct {
	ID           string          `json:"listing_id"`
	PublisherID  string          `json:"publisher_account_id"`
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Version      string          `json:"version"`
	Capabilities []string        `json:"capabilities"`
	Manifest     json.RawMessage `json:"manifest"`
	PriceMinor   int64           `json:"price_minor"`
	Currency     string          `json:"currency"`
	CommissionBP int             `json:"commission_basis_points"`
	Status       string          `json:"status"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type MarketplaceTransaction struct {
	ID              string          `json:"transaction_id"`
	ListingID       string          `json:"listing_id"`
	BuyerAccountID  string          `json:"buyer_account_id"`
	SellerAccountID string          `json:"seller_account_id"`
	BuyerAgentID    string          `json:"buyer_agent_id,omitempty"`
	SellerAgentID   string          `json:"seller_agent_id,omitempty"`
	TaskID          string          `json:"task_id,omitempty"`
	Quantity        int64           `json:"quantity"`
	AmountMinor     int64           `json:"amount_minor"`
	CommissionMinor int64           `json:"commission_minor"`
	Currency        string          `json:"currency"`
	Status          string          `json:"status"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type MarketplaceInstallation struct {
	ID          string    `json:"installation_id"`
	ListingID   string    `json:"listing_id"`
	AccountID   string    `json:"account_id"`
	Version     string    `json:"version"`
	Status      string    `json:"status"`
	InstalledAt time.Time `json:"installed_at"`
}
