package httpapi

import (
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"html/template"
	"net/http"
)

func (s *Server) registerRefundRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/billing/invoices/{invoiceID}/refunds", s.requestRefund)
	mux.HandleFunc("POST /v1/billing/invoices/{invoiceID}/refunds/{refundID}/reconcile", s.queryRefund)
	mux.HandleFunc("GET /v1/billing/invoices/{invoiceID}/statement", s.invoiceStatement)
	mux.HandleFunc("GET /v1/treasury/receivables/{invoiceID}/statement", s.invoiceStatement)
	mux.HandleFunc("POST /v1/treasury/receivables/{invoiceID}/refunds/{refundID}/{action}", s.merchantRefund)
}
func (s *Server) requestRefund(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeInvoiceScope(w, r, "billing:write")
	if !ok {
		return
	}
	var in struct {
		PaymentID string `json:"payment_id"`
		Amount    int64  `json:"amount_minor"`
		Reason    string `json:"reason"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	d, err := s.billing.RequestRefund(r.Context(), c.AccountID, r.PathValue("invoiceID"), in.PaymentID, in.Amount, in.Reason, r.Header.Get("Idempotency-Key"))
	refundResponse(w, d, err)
}
func refundResponse(w http.ResponseWriter, d protocol.BillingDocument, err error) {
	if err != nil {
		billingError(w, err)
		return
	}
	hideCheckout(&d)
	writeJSON(w, 200, d)
}
func (s *Server) queryRefund(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeInvoiceScope(w, r, "billing:write")
	if !ok {
		return
	}
	d, err := s.billing.ReconcileRefund(r.Context(), c.AccountID, r.PathValue("invoiceID"), r.PathValue("refundID"))
	refundResponse(w, d, err)
}
func (s *Server) merchantRefund(w http.ResponseWriter, r *http.Request) {
	scope := "treasury:write"
	if r.PathValue("action") == "decision" {
		scope = "treasury:approve"
	}
	c, ok := s.treasuryAuth(w, r, scope)
	if !ok {
		return
	}
	d, err := s.store.GetBillByID(r.Context(), r.PathValue("invoiceID"))
	if err != nil {
		billingError(w, billing.ErrNotFound)
		return
	}
	ctx := billing.WithActor(r.Context(), c.AccountID)
	id := r.PathValue("refundID")
	switch r.PathValue("action") {
	case "decision":
		var in struct{ Decision, Reason string }
		if !s.decodeJSON(w, r, &in) {
			return
		}
		d, err = s.billing.DecideRefund(ctx, d.Invoice.AccountID, d.Invoice.ID, id, in.Decision, in.Reason)
	case "submit":
		d, err = s.billing.SubmitRefund(ctx, d.Invoice.AccountID, d.Invoice.ID, id)
	case "reconcile":
		d, err = s.billing.ReconcileRefund(ctx, d.Invoice.AccountID, d.Invoice.ID, id)
	case "simulate":
		var in struct{ Outcome string }
		if !s.decodeJSON(w, r, &in) {
			return
		}
		d, err = s.billing.SimulateRefund(ctx, d.Invoice.AccountID, d.Invoice.ID, id, in.Outcome)
	default:
		http.NotFound(w, r)
		return
	}
	refundResponse(w, d, err)
}

var statementTemplate = template.Must(template.New("statement").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>账单与退款凭证</title><style>body{font:16px system-ui;max-width:960px;margin:40px auto}td,th{padding:8px;border-bottom:1px solid #ccc}table{width:100%}</style><h1>账单与退款凭证</h1><p>本凭证不是税务发票，也不是独立银行到账证明。{{if .Invoice.TestMode}}本地模拟，不发生真实资金转移。{{end}}</p><p>{{.Invoice.Number}} · {{.Invoice.Status}} · {{.Invoice.Currency}} · 金额（最小货币单位）：{{.Invoice.AmountMinor}}</p><p>服务租户：{{.Invoice.OrganizationID}} / 付款责任租户：{{.Invoice.PayerOrganizationID}}</p><h2>消费明细</h2><table>{{range .Invoice.Lines}}<tr><td>{{.Description}}</td><td>{{.Quantity}} {{.Unit}}</td><td>{{.AmountMinor}}</td></tr>{{end}}</table><h2>付款回执</h2>{{range .Receipts}}<p>{{.ID}} · {{.AmountMinor}} {{.Currency}} · {{.Provider}} · {{.ProviderTransactionID}} · {{.PaidAt}}</p>{{end}}<h2>原路退款</h2>{{range .Refunds}}<p>{{.ID}} · {{.Status}} · {{.AmountMinor}} {{.Currency}} · {{.ProviderRefundID}} · {{.Reason}}</p>{{end}}<h2>退款贷项凭证</h2>{{range .CreditNotes}}<p>{{.ID}} · 退款 {{.RefundID}} · {{.AmountMinor}} {{.Currency}} · {{.CreatedAt}}</p>{{end}}<p>原账单及付款回执保持不变，退款以独立贷项记录反映。</p></html>`))

func (s *Server) invoiceStatement(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeInvoiceScope(w, r, "billing:read")
	if !ok {
		return
	}
	d, err := s.store.GetBill(r.Context(), c.AccountID, r.PathValue("invoiceID"))
	if err != nil {
		billingError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_ = statementTemplate.Execute(w, d)
}
