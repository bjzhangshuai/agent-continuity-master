package protocol

import "time"

// Money is always expressed in currency minor units; no floating-point money.
type UsageTotal struct {
	Metric   string `json:"metric"`
	Unit     string `json:"unit"`
	Quantity int64  `json:"quantity"`
	Records  int64  `json:"records"`
}

type BillingRate struct {
	Metric         string `json:"metric"`
	Description    string `json:"description"`
	Unit           string `json:"unit"`
	UnitSize       int64  `json:"unit_size"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
}

type BillingLine struct {
	BillingRate
	Quantity    int64 `json:"quantity"`
	Records     int64 `json:"records"`
	AmountMinor int64 `json:"amount_minor"`
}

type BillingInvoice struct {
	IssuedBy            string        `json:"issued_by,omitempty"`
	PayerOrganizationID string        `json:"payer_organization_id,omitempty"`
	PaymentMandateID    string        `json:"payment_mandate_id,omitempty"`
	ID                  string        `json:"invoice_id"`
	AccountID           string        `json:"account_id"`
	BillingAccountID    string        `json:"billing_account_id"`
	OrganizationID      string        `json:"organization_id,omitempty"`
	Number              string        `json:"number"`
	Status              string        `json:"status"`
	Currency            string        `json:"currency"`
	AmountMinor         int64         `json:"amount_minor"`
	PriceBook           string        `json:"price_book"`
	TestMode            bool          `json:"test_mode"`
	PeriodStart         time.Time     `json:"period_start"`
	PeriodEnd           time.Time     `json:"period_end"`
	DueAt               time.Time     `json:"due_at"`
	CreatedAt           time.Time     `json:"created_at"`
	Lines               []BillingLine `json:"lines"`
	IdempotencyKey      string        `json:"idempotency_key"`
}

type BillingPayment struct {
	InitiatedBy           string     `json:"initiated_by,omitempty"`
	ID                    string     `json:"payment_id"`
	Provider              string     `json:"provider"`
	Environment           string     `json:"environment"`
	Status                string     `json:"status"`
	AmountMinor           int64      `json:"amount_minor"`
	Currency              string     `json:"currency"`
	ProviderOrderID       string     `json:"provider_order_id,omitempty"`
	ProviderTransactionID string     `json:"provider_transaction_id,omitempty"`
	CheckoutURL           string     `json:"checkout_url,omitempty"`
	QRCode                string     `json:"qr_code,omitempty"`
	IdempotencyKey        string     `json:"idempotency_key"`
	CreatedAt             time.Time  `json:"created_at"`
	ExpiresAt             time.Time  `json:"expires_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	LastCheckedAt         *time.Time `json:"last_checked_at,omitempty"`
	ReconcileAttempts     int        `json:"reconcile_attempts"`
	LastError             string     `json:"last_error,omitempty"`
	LinkRevoked           bool       `json:"link_revoked"`
}

type PaymentReceipt struct {
	ID                    string    `json:"receipt_id"`
	InvoiceID             string    `json:"invoice_id"`
	PaymentID             string    `json:"payment_id"`
	Provider              string    `json:"provider"`
	ProviderTransactionID string    `json:"provider_transaction_id"`
	AmountMinor           int64     `json:"amount_minor"`
	Currency              string    `json:"currency"`
	PaidAt                time.Time `json:"paid_at"`
	Environment           string    `json:"environment"`
	Kind                  string    `json:"kind"` // payment receipt, not a tax invoice.
}

type BillingEvent struct {
	ID        string    `json:"event_id"`
	PaymentID string    `json:"payment_id,omitempty"`
	Kind      string    `json:"kind"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

// An invoice is the transactional aggregate. CAS commits payment state, receipts
// and its audit trail atomically. Indexed routing columns live in separate tables.
type BillingDocument struct {
	Invoice  BillingInvoice   `json:"invoice"`
	Payments []BillingPayment `json:"payments"`
	Receipts []PaymentReceipt `json:"receipts"`
	Events   []BillingEvent   `json:"events"`
	Version  int64            `json:"version"`
}

type PaymentReconcileTarget struct {
	AccountID string
	InvoiceID string
	PaymentID string
}
