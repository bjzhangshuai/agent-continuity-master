package billing

import (
	"context"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"time"
)

var ErrNotFound = errors.New("billing record not found")
var ErrConflict = errors.New("billing state changed or idempotency key conflicts; reload before retrying")
var ErrOverlap = errors.New("this usage period overlaps an existing non-void invoice")
var ErrInvalid = errors.New("invalid billing operation")

type Repository interface {
	SummarizeUsage(context.Context, string, string, time.Time, time.Time) ([]protocol.UsageTotal, error)
	CreateBill(context.Context, protocol.BillingDocument) (protocol.BillingDocument, error)
	SaveBill(context.Context, protocol.BillingDocument, int64) error
	GetBill(context.Context, string, string) (protocol.BillingDocument, error)
	ListBills(context.Context, string, int) ([]protocol.BillingDocument, error)
	FindPayment(context.Context, string, string) (protocol.BillingDocument, error)
	DuePayments(context.Context, time.Time, int) ([]protocol.PaymentReconcileTarget, error)
}

func SameRequest(a, b protocol.BillingInvoice) bool {
	return a.AccountID == b.AccountID && a.BillingAccountID == b.BillingAccountID && a.PeriodStart.Equal(b.PeriodStart) && a.PeriodEnd.Equal(b.PeriodEnd)
}
func Overlaps(a, b protocol.BillingInvoice) bool {
	return (a.OrganizationID != "" || a.AccountID == b.AccountID) && a.OrganizationID == b.OrganizationID && a.TestMode == b.TestMode && a.Status != "void" && b.Status != "void" && a.PeriodStart.Before(b.PeriodEnd) && b.PeriodStart.Before(a.PeriodEnd)
}
