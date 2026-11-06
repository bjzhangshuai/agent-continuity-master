package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/commerce"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/agent-continuity/agent-continuity-master/internal/orchestration"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/http"
)

func WithOrchestration(agents map[string]orchestration.Executor) Option {
	return func(s *Server) { s.orchestrator = orchestration.New(s.store, agents) }
}
func WithCommerce(g commerce.Gateway, simulation bool) Option {
	return func(s *Server) { s.commerce = &commerce.Service{Repo: s.store, Gateway: g, Simulation: simulation} }
}
func (s *Server) registerWorkflowRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/orchestration/agents", s.executionAgents)
	mux.HandleFunc("POST /v1/orchestration/runs", s.createRun)
	mux.HandleFunc("GET /v1/orchestration/runs", s.listRuns)
	mux.HandleFunc("GET /v1/orchestration/runs/{runID}", s.getRun)
	mux.HandleFunc("POST /v1/orchestration/runs/{runID}/{action}", s.controlRun)
	mux.HandleFunc("POST /v1/marketplace/transactions/{transactionID}/checkout", s.marketplaceCheckout)
	mux.HandleFunc("POST /v1/marketplace/transactions/{transactionID}/settlement", s.createSettlement)
	mux.HandleFunc("GET /v1/commerce/settlements", s.listSettlements)
	mux.HandleFunc("GET /v1/commerce/settlements/{transactionID}", s.getSettlement)
	mux.HandleFunc("POST /v1/commerce/settlements/{transactionID}/reconcile", s.reconcileSettlement)
	mux.HandleFunc("POST /v1/commerce/settlements/{transactionID}/withdraw", s.withdrawSettlement)
	mux.HandleFunc("POST /v1/commerce/recipients", s.createRecipient)
	mux.HandleFunc("POST /v1/commerce/recipients/{recipientID}/reconcile", s.verifyRecipient)
	mux.HandleFunc("POST /v1/billing/invoices/{invoiceID}/fiscal", s.issueFiscal)
	mux.HandleFunc("GET /v1/commerce/fiscal", s.listFiscal)
	mux.HandleFunc("GET /v1/billing/invoices/{invoiceID}/fiscal", s.listInvoiceFiscal)
	mux.HandleFunc("GET /v1/commerce/fiscal/{fiscalID}", s.getFiscal)
	mux.HandleFunc("POST /v1/commerce/fiscal/{fiscalID}/reconcile", s.reconcileFiscal)
	mux.HandleFunc("GET /v1/commerce/fiscal/{fiscalID}/document", s.fiscalDocument)
}
func workflowError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, operations.ErrNotFound):
		writeError(w, 404, "not_found", "operation not found")
	case errors.Is(err, operations.ErrConflict):
		writeError(w, 409, "operation_conflict", err.Error())
	case errors.Is(err, operations.ErrInvalid):
		writeError(w, 400, "invalid_operation", err.Error())
	default:
		billingError(w, err)
	}
}
func (s *Server) executionAgents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeScope(w, r, "tasks:read"); !ok {
		return
	}
	names := []string{}
	if s.orchestrator != nil {
		for name := range s.orchestrator.Agents {
			names = append(names, name)
		}
	}
	writeJSON(w, 200, map[string]any{"agents": names})
}
func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "tasks:write")
	if !ok {
		return
	}
	if s.orchestrator == nil {
		writeError(w, 503, "disabled", "orchestration not configured")
		return
	}
	var q orchestration.Request
	if !s.decodeJSON(w, r, &q) {
		return
	}
	q.OrganizationID = ""
	if q.TaskID != "" {
		allowed, err := s.taskAccessAllowed(r, c, q.TaskID, protocol.PermissionWrite)
		if err != nil || !allowed {
			writeError(w, 403, "task_forbidden", "task write access required")
			return
		}
		task, err := s.store.GetTask(r.Context(), q.TaskID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if task.AccountID != c.AccountID {
			writeError(w, 403, "task_owner_required", "execution budget requires task owner")
			return
		}
		q.OrganizationID = task.OrganizationID
	}
	value, err := s.orchestrator.Create(r.Context(), c.AccountID, r.Header.Get("Idempotency-Key"), q)
	if err != nil {
		workflowError(w, err)
		return
	}
	s.audit(r, c, "orchestration.created", "run", value.ID, nil)
	writeJSON(w, 201, value)
}
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	s.listWorkflow(w, r, "run", "tasks:read")
}
func (s *Server) listWorkflow(w http.ResponseWriter, r *http.Request, kind, scope string) {
	c, ok := s.authorizeScope(w, r, scope)
	if !ok {
		return
	}
	rows, err := s.store.ListOperations(r.Context(), kind, c.AccountID, r.URL.Query().Get("after"), 100)
	if err != nil {
		workflowError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "tasks:read")
	if !ok {
		return
	}
	if s.orchestrator == nil {
		writeError(w, 503, "disabled", "orchestration not configured")
		return
	}
	v, err := s.orchestrator.Get(r.Context(), c.AccountID, r.PathValue("runID"))
	if err != nil {
		workflowError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) controlRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "tasks:write")
	if !ok {
		return
	}
	if s.orchestrator == nil {
		writeError(w, 503, "disabled", "orchestration not configured")
		return
	}
	v, err := s.orchestrator.Control(r.Context(), c.AccountID, r.PathValue("runID"), r.PathValue("action"))
	if err != nil {
		workflowError(w, err)
		return
	}
	s.audit(r, c, "orchestration."+r.PathValue("action"), "run", v.ID, nil)
	writeJSON(w, 200, v)
}
func (s *Server) marketplaceCheckout(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "marketplace:transact")
	if !ok {
		return
	}
	var q struct {
		BillingAccountID string `json:"billing_account_id"`
		Provider         string `json:"provider"`
	}
	if !s.decodeJSON(w, r, &q) {
		return
	}
	t, err := s.store.GetMarketplaceTransaction(r.Context(), r.PathValue("transactionID"))
	if err != nil || t.BuyerAccountID != c.AccountID || t.Status != "authorized" {
		writeError(w, 409, "invalid_transaction", "buyer must authorize transaction first")
		return
	}
	accounts, err := s.store.ListBillingAccounts(r.Context(), c.AccountID)
	var b protocol.BillingAccount
	for _, candidate := range accounts {
		if candidate.ID == q.BillingAccountID {
			b = candidate
		}
	}
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if b.OrganizationID != "" && !tenantFinanceRole(s.tenantRole(r, c, b.OrganizationID), true) {
		writeError(w, 403, "tenant_billing_forbidden", "tenant finance role required")
		return
	}
	if err = s.applyPayer(r, &b); err != nil {
		writeStorageError(w, err)
		return
	}
	d, err := s.billing.IssuePurchase(billing.WithActor(r.Context(), c.AccountID), t, b)
	if err != nil {
		billingError(w, err)
		return
	}
	if !s.canAccessInvoice(r, c, d.Invoice, true) {
		// Re-check current membership after issuing the bill. Both originating and
		// delegated payer finance retain the existing invoice payment policy.
		writeJSON(w, 201, map[string]any{"bill": d, "payment_id": "", "payment_link": "", "requires_payer": true})
		return
	}
	d, link, err := s.billing.CreatePayment(billing.WithActor(r.Context(), c.AccountID), c.AccountID, d.Invoice.ID, q.Provider, r.Header.Get("Idempotency-Key"))
	if err != nil {
		billingError(w, err)
		return
	}
	paymentID := link
	for _, p := range d.Payments {
		if p.ID == paymentID {
			link = s.billing.Link(p)
		}
	}
	writeJSON(w, 201, map[string]any{"bill": d, "payment_id": paymentID, "payment_link": link})
}
func (s *Server) commerceEnabled(w http.ResponseWriter) bool {
	if s.commerce == nil || s.commerce.Gateway == nil {
		writeError(w, 503, "commerce_disabled", "commerce gateway not configured")
		return false
	}
	return true
}
func (s *Server) createSettlement(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "marketplace:transact")
	if !ok || !s.commerceEnabled(w) {
		return
	}
	t, err := s.store.GetMarketplaceTransaction(r.Context(), r.PathValue("transactionID"))
	if err != nil || t.BuyerAccountID != c.AccountID && t.SellerAccountID != c.AccountID {
		writeError(w, 404, "not_found", "transaction not found")
		return
	}
	id := operations.ID("inv", t.BuyerAccountID, "marketplace:"+t.ID)
	d, err := s.store.GetBill(r.Context(), t.BuyerAccountID, id)
	if err != nil {
		billingError(w, err)
		return
	}
	v, err := s.commerce.CreateSettlement(r.Context(), t, d)
	if err != nil {
		workflowError(w, err)
		return
	}
	writeJSON(w, 201, v)
}
func (s *Server) listSettlements(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:read")
	if !ok || !s.commerceEnabled(w) {
		return
	}
	rows, err := s.store.ListOperations(r.Context(), "settlement", c.AccountID, r.URL.Query().Get("after"), 100)
	if err != nil {
		workflowError(w, err)
		return
	}
	items := []commerce.Settlement{}
	for _, o := range rows {
		v, e := s.commerce.Load(r.Context(), c.AccountID, o.ID)
		if e != nil {
			workflowError(w, e)
			return
		}
		items = append(items, v)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) getSettlement(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:read")
	if !ok || !s.commerceEnabled(w) {
		return
	}
	v, e := s.commerce.Load(r.Context(), c.AccountID, r.PathValue("transactionID"))
	if e != nil {
		workflowError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) reconcileSettlement(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:write")
	if !ok || !s.commerceEnabled(w) {
		return
	}
	v, e := s.commerce.Reconcile(r.Context(), c.AccountID, r.PathValue("transactionID"))
	if e != nil {
		workflowError(w, e)
		return
	}
	s.audit(r, c, "commerce.reconciled", "settlement", v.TransactionID, nil)
	writeJSON(w, 200, v)
}
func (s *Server) withdrawSettlement(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:write")
	if !ok || !s.commerceEnabled(w) {
		return
	}
	var q struct {
		Amount      int64  `json:"amount_minor"`
		Destination string `json:"destination"`
	}
	if !s.decodeJSON(w, r, &q) {
		return
	}
	v, e := s.commerce.Withdraw(r.Context(), c.AccountID, r.PathValue("transactionID"), r.Header.Get("Idempotency-Key"), q.Destination, q.Amount)
	if e != nil {
		workflowError(w, e)
		return
	}
	s.audit(r, c, "commerce.withdrawal_reserved", "settlement", v.TransactionID, nil)
	writeJSON(w, 202, v)
}
func (s *Server) createRecipient(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:write")
	if !ok || !s.commerceEnabled(w) {
		return
	}
	var q struct {
		Token string `json:"provider_account_token"`
		Kind  string `json:"kind"`
	}
	if !s.decodeJSON(w, r, &q) {
		return
	}
	v, e := s.commerce.Recipient(r.Context(), c.AccountID, q.Token, q.Kind)
	if e != nil {
		workflowError(w, e)
		return
	}
	writeJSON(w, 201, v)
}
func (s *Server) verifyRecipient(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:write")
	if !ok || !s.commerceEnabled(w) {
		return
	}
	v, e := s.commerce.VerifyRecipient(r.Context(), c.AccountID, r.PathValue("recipientID"))
	if e != nil {
		workflowError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) issueFiscal(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeInvoiceScope(w, r, "billing:write")
	if !ok || !s.commerceEnabled(w) {
		return
	}
	var q commerce.FiscalRequest
	if !s.decodeJSON(w, r, &q) {
		return
	}
	v, e := s.commerce.IssueFiscal(r.Context(), c.AccountID, r.PathValue("invoiceID"), q)
	if e != nil {
		workflowError(w, e)
		return
	}
	s.audit(r, c, "fiscal.requested", "fiscal", v.Effect.ID, nil)
	writeJSON(w, 202, v)
}
func (s *Server) listFiscal(w http.ResponseWriter, r *http.Request) {
	s.listWorkflow(w, r, "fiscal", "billing:read")
}
func (s *Server) getFiscal(w http.ResponseWriter, r *http.Request)      { s.fiscalRead(w, r, false) }
func (s *Server) fiscalDocument(w http.ResponseWriter, r *http.Request) { s.fiscalRead(w, r, true) }
func (s *Server) fiscalRead(w http.ResponseWriter, r *http.Request, download bool) {
	c, ok := s.authorizeScope(w, r, "billing:read")
	if !ok {
		return
	}
	o, e := s.store.GetOperation(r.Context(), "fiscal", r.PathValue("fiscalID"))
	if e != nil {
		writeError(w, 404, "not_found", "fiscal document not found")
		return
	}
	var v commerce.Fiscal
	if e = json.Unmarshal(o.Data, &v); e != nil {
		workflowError(w, e)
		return
	}
	bill, e := s.store.GetBill(r.Context(), o.Owner, v.InvoiceID)
	if e != nil || !s.canAccessInvoice(r, c, bill.Invoice, false) {
		writeError(w, 404, "not_found", "fiscal document not found")
		return
	}
	if download {
		if v.Result.Status != "succeeded" {
			writeError(w, 409, "not_issued", "document is not issued")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="fiscal-document.json"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(v.Result.Document))
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) reconcileFiscal(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:write")
	if !ok || !s.commerceEnabled(w) {
		return
	}
	o, e := s.store.GetOperation(r.Context(), "fiscal", r.PathValue("fiscalID"))
	if e != nil {
		workflowError(w, e)
		return
	}
	var f commerce.Fiscal
	if e = json.Unmarshal(o.Data, &f); e != nil {
		workflowError(w, e)
		return
	}
	bill, e := s.store.GetBill(r.Context(), o.Owner, f.InvoiceID)
	if e != nil || !s.canAccessInvoice(r, c, bill.Invoice, true) {
		writeError(w, 404, "not_found", "fiscal document not found")
		return
	}
	v, e := s.commerce.ReconcileFiscal(r.Context(), o.Owner, r.PathValue("fiscalID"))
	if e != nil {
		workflowError(w, e)
		return
	}
	s.audit(r, c, "fiscal.reconciled", "fiscal", v.Effect.ID, nil)
	writeJSON(w, 200, v)
}

func (s *Server) listInvoiceFiscal(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeInvoiceScope(w, r, "billing:read")
	if !ok {
		return
	}
	bill, e := s.store.GetBill(r.Context(), c.AccountID, r.PathValue("invoiceID"))
	if e != nil {
		workflowError(w, e)
		return
	}
	ids := []string{operations.ID("fiscal", c.AccountID, bill.Invoice.ID)}
	for _, refund := range bill.Refunds {
		ids = append(ids, operations.ID("fiscal", c.AccountID, "credit:"+refund.ID))
	}
	items := []operations.Record{}
	for _, id := range ids {
		o, err := s.store.GetOperation(r.Context(), "fiscal", id)
		if errors.Is(err, operations.ErrNotFound) {
			continue
		}
		if err != nil {
			workflowError(w, err)
			return
		}
		items = append(items, o)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
