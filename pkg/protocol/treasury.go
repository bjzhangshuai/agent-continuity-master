package protocol

import "time"

// Treasury is the operator's outgoing-money workflow, not a payer's wallet.
// Destination identifiers are opaque; bank numbers and payment secrets are never stored here.
type TreasuryDestination struct {
	ID            string `json:"destination_id"`
	Label         string `json:"label"`
	Kind          string `json:"kind"`
	Currency      string `json:"currency"`
	MaskedAccount string `json:"masked_account"`
	Environment   string `json:"environment"`
	Verified      bool   `json:"verified"`
}

type TreasuryCollection struct {
	ID                    string     `json:"collection_id"`
	InvoiceID             string     `json:"invoice_id"`
	InvoiceNumber         string     `json:"invoice_number"`
	PaymentID             string     `json:"payment_id"`
	ReceiptID             string     `json:"receipt_id"`
	ProviderTransactionID string     `json:"provider_transaction_id"`
	Currency              string     `json:"currency"`
	GrossMinor            int64      `json:"gross_minor"`
	FeeMinor              int64      `json:"fee_minor"`
	NetMinor              int64      `json:"net_minor"`
	Status                string     `json:"status"`
	Environment           string     `json:"environment"`
	ReceivedAt            time.Time  `json:"received_at"`
	SettledAt             *time.Time `json:"settled_at,omitempty"`
}

type TreasuryPayout struct {
	ID               string              `json:"payout_id"`
	Destination      TreasuryDestination `json:"destination"`
	AmountMinor      int64               `json:"amount_minor"`
	Currency         string              `json:"currency"`
	Provider         string              `json:"provider"`
	Environment      string              `json:"environment"`
	Status           string              `json:"status"`
	IdempotencyKey   string              `json:"idempotency_key"`
	RequestedBy      string              `json:"requested_by"`
	ApprovedBy       string              `json:"approved_by,omitempty"`
	ProviderPayoutID string              `json:"provider_payout_id,omitempty"`
	ReceiptID        string              `json:"receipt_id,omitempty"`
	LastError        string              `json:"last_error,omitempty"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
	SubmittedAt      *time.Time          `json:"submitted_at,omitempty"`
	PaidAt           *time.Time          `json:"paid_at,omitempty"`
	ReturnedAt       *time.Time          `json:"returned_at,omitempty"`
	LastCheckedAt    *time.Time          `json:"last_checked_at,omitempty"`
}

type TreasuryEvent struct {
	ID         string    `json:"event_id"`
	ResourceID string    `json:"resource_id"`
	Kind       string    `json:"kind"`
	Actor      string    `json:"actor"`
	Detail     string    `json:"detail"`
	CreatedAt  time.Time `json:"created_at"`
}

type TreasuryDocument struct {
	OwnerAccountID string                `json:"owner_account_id"`
	Destinations   []TreasuryDestination `json:"destinations"`
	Collections    []TreasuryCollection  `json:"collections"`
	Payouts        []TreasuryPayout      `json:"payouts"`
	Events         []TreasuryEvent       `json:"events"`
	Version        int64                 `json:"version"`
}

type TreasuryBalance struct {
	Currency       string `json:"currency"`
	Environment    string `json:"environment"`
	GrossMinor     int64  `json:"gross_minor"`
	FeeMinor       int64  `json:"fee_minor"`
	PendingMinor   int64  `json:"pending_minor"`
	AvailableMinor int64  `json:"available_minor"`
	ReservedMinor  int64  `json:"reserved_minor"`
	PaidMinor      int64  `json:"paid_minor"`
	ReturnedMinor  int64  `json:"returned_minor"`
}
