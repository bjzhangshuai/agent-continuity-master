package httpapi

import (
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/internal/qrcode"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func WithPayments(providers *payments.Registry, book billing.PriceBook, baseURL string) Option {
	return func(s *Server) {
		key := sha256.Sum256([]byte("payment-links:" + s.bootstrapToken))
		s.billing = billing.New(s.store, providers, book, baseURL, key[:])
	}
}
func (s *Server) registerBillingRoutes(mux *http.ServeMux) {
	s.registerRefundRoutes(mux)
	mux.HandleFunc("GET /v1/billing/providers", s.paymentProviders)
	mux.HandleFunc("POST /v1/billing/quote", s.quoteInvoice)
	mux.HandleFunc("POST /v1/billing/invoices", s.issueInvoice)
	mux.HandleFunc("GET /v1/billing/invoices", s.listInvoices)
	mux.HandleFunc("GET /v1/billing/invoices/{invoiceID}", s.getInvoice)
	mux.HandleFunc("GET /v1/billing/invoices/{invoiceID}/export", s.exportInvoice)
	mux.HandleFunc("GET /v1/billing/invoices/{invoiceID}/receipts/{receiptID}", s.getReceipt)
	mux.HandleFunc("POST /v1/billing/invoices/{invoiceID}/void", s.voidInvoice)
	mux.HandleFunc("POST /v1/billing/invoices/{invoiceID}/payments", s.createPayment)
	mux.HandleFunc("POST /v1/billing/invoices/{invoiceID}/payments/{paymentID}/reconcile", s.reconcilePayment)
	mux.HandleFunc("POST /v1/billing/invoices/{invoiceID}/payments/{paymentID}/revoke-link", s.revokePaymentLink)
	mux.HandleFunc("POST /v1/billing/invoices/{invoiceID}/payments/{paymentID}/simulate", s.simulatePayment)
	mux.HandleFunc("POST /v1/payments/webhooks/{provider}", s.paymentWebhook)
	mux.HandleFunc("GET /pay/{paymentID}", s.checkoutPage)
	mux.HandleFunc("GET /pay/{paymentID}/qr", s.checkoutQR)
	mux.HandleFunc("POST /pay/{paymentID}/confirm", s.confirmCheckout)
}
func billingError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	code := "payment_gateway_error"
	message := "Payment gateway unavailable or outcome uncertain; reconcile the existing payment before retrying."
	if payments.DefinitiveRejection(err) {
		writeError(w, http.StatusUnprocessableEntity, "checkout_rejected", "Provider rejected checkout; check supported currency, amount limits and merchant configuration.")
		return
	}
	switch {
	case errors.Is(err, billing.ErrNotFound):
		status = http.StatusNotFound
		code = "billing_not_found"
		message = err.Error()
	case errors.Is(err, billing.ErrConflict), errors.Is(err, billing.ErrOverlap):
		status = http.StatusConflict
		code = "billing_conflict"
		message = err.Error()
	case errors.Is(err, billing.ErrInvalid):
		status = http.StatusBadRequest
		code = "invalid_billing_operation"
		message = err.Error()
	}
	writeError(w, status, code, message)
}
func (s *Server) paymentProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeScope(w, r, "billing:read"); !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"providers": s.billing.Providers.List(), "price_book": s.billing.PriceBook, "capabilities": []string{"usage_quote", "immutable_invoice", "payment_link", "receipt", "csv_export", "signed_webhooks", "manual_reconciliation"}})
}

type invoiceInput struct {
	BillingAccountID string    `json:"billing_account_id"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
}

func (s *Server) invoiceRequest(w http.ResponseWriter, r *http.Request, issue bool) {
	claims, ok := s.authorizeScope(w, r, "billing:write")
	if !ok {
		return
	}
	var input invoiceInput
	if !s.decodeJSON(w, r, &input) {
		return
	}
	accounts, err := s.store.ListBillingAccounts(r.Context(), claims.AccountID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	var account *protocol.BillingAccount
	for _, a := range accounts {
		if a.ID == input.BillingAccountID && a.AccountID == claims.AccountID && a.Status == "active" {
			copy := a
			account = &copy
		}
	}
	if account == nil {
		billingError(w, billing.ErrNotFound)
		return
	}
	if account.OrganizationID != "" && !tenantFinanceRole(s.tenantRole(r, claims, account.OrganizationID), true) {
		writeError(w, 403, "tenant_billing_forbidden", "Active tenant finance membership required")
		return
	}
	if err = s.applyPayer(r, account); err != nil {
		writeStorageError(w, err)
		return
	}
	input.PeriodStart = input.PeriodStart.UTC().Truncate(time.Microsecond)
	input.PeriodEnd = input.PeriodEnd.UTC().Truncate(time.Microsecond)
	if !issue {
		v, err := s.billing.Quote(r.Context(), *account, input.PeriodStart, input.PeriodEnd)
		if err != nil {
			billingError(w, err)
			return
		}
		writeJSON(w, 200, v)
		return
	}
	d, err := s.billing.Issue(billing.WithActor(r.Context(), claims.AccountID), *account, input.PeriodStart, input.PeriodEnd, r.Header.Get("Idempotency-Key"))
	if err != nil {
		billingError(w, err)
		return
	}
	writeJSON(w, 201, d)
}
func (s *Server) quoteInvoice(w http.ResponseWriter, r *http.Request) { s.invoiceRequest(w, r, false) }
func (s *Server) issueInvoice(w http.ResponseWriter, r *http.Request) { s.invoiceRequest(w, r, true) }
func (s *Server) listInvoices(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "billing:read")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	d, err := s.store.ListBills(r.Context(), claims.AccountID, limit)
	if err != nil {
		billingError(w, err)
		return
	}
	visible := []protocol.BillingDocument{}
	for _, doc := range d {
		if s.canAccessInvoice(r, claims, doc.Invoice, false) {
			if !auth.HasScope(claims, "billing:write") || !s.canAccessInvoice(r, claims, doc.Invoice, true) {
				hideCheckout(&doc)
			}
			visible = append(visible, doc)
		}
	}
	writeJSON(w, 200, map[string]any{"invoices": visible})
}
func (s *Server) getInvoice(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeInvoiceScope(w, r, "billing:read")
	if !ok {
		return
	}
	d, err := s.store.GetBill(r.Context(), claims.AccountID, r.PathValue("invoiceID"))
	if err != nil {
		billingError(w, err)
		return
	}
	links := map[string]string{}
	canPay := s.invoiceCanPay(r, d.Invoice)
	if !canPay {
		hideCheckout(&d)
	}
	for _, p := range d.Payments {
		if canPay && !p.LinkRevoked && time.Now().Before(p.ExpiresAt) {
			links[p.ID] = s.billing.Link(p)
		}
	}
	writeJSON(w, 200, map[string]any{"invoice": d.Invoice, "payments": d.Payments, "receipts": d.Receipts, "refunds": d.Refunds, "credit_notes": d.CreditNotes, "events": d.Events, "version": d.Version, "payment_links": links, "can_pay": canPay})
}
func (s *Server) createPayment(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeInvoiceScope(w, r, "billing:write")
	if !ok {
		return
	}
	var input struct {
		Provider string `json:"provider"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	d, id, err := s.billing.CreatePayment(r.Context(), claims.AccountID, r.PathValue("invoiceID"), input.Provider, r.Header.Get("Idempotency-Key"))
	if err != nil {
		billingError(w, err)
		return
	}
	for _, p := range d.Payments {
		if p.ID == id {
			writeJSON(w, 201, map[string]any{"payment": p, "payment_link": s.billing.Link(p), "invoice": d.Invoice})
			return
		}
	}
	billingError(w, billing.ErrNotFound)
}
func (s *Server) reconcilePayment(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeInvoiceScope(w, r, "billing:write")
	if !ok {
		return
	}
	d, err := s.billing.Reconcile(r.Context(), claims.AccountID, r.PathValue("invoiceID"), r.PathValue("paymentID"), true)
	if err != nil {
		billingError(w, err)
		return
	}
	writeJSON(w, 200, d)
}
func (s *Server) revokePaymentLink(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeInvoiceScope(w, r, "billing:write")
	if !ok {
		return
	}
	d, err := s.billing.RevokeLink(r.Context(), claims.AccountID, r.PathValue("invoiceID"), r.PathValue("paymentID"))
	if err != nil {
		billingError(w, err)
		return
	}
	writeJSON(w, 200, d)
}
func (s *Server) voidInvoice(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeInvoiceScope(w, r, "billing:write")
	if !ok {
		return
	}
	d, err := s.billing.Void(r.Context(), claims.AccountID, r.PathValue("invoiceID"))
	if err != nil {
		billingError(w, err)
		return
	}
	writeJSON(w, 200, d)
}
func (s *Server) simulatePayment(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeInvoiceScope(w, r, "billing:write")
	if !ok {
		return
	}
	var input struct {
		Outcome string `json:"outcome"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	d, err := s.billing.Simulate(r.Context(), claims.AccountID, r.PathValue("invoiceID"), r.PathValue("paymentID"), input.Outcome)
	if err != nil {
		billingError(w, err)
		return
	}
	writeJSON(w, 200, d)
}
func (s *Server) paymentWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		writeError(w, 413, "webhook_too_large", "Payment notification exceeds size limit")
		return
	}
	_, err = s.billing.Notify(r.Context(), r.PathValue("provider"), r.Header, body)
	if err != nil && !errors.Is(err, payments.ErrUnsupported) {
		s.logger.Warn("payment notification not accepted", "provider", r.PathValue("provider"), "error", err)
		if errors.Is(err, payments.ErrSignature) {
			writeError(w, 400, "invalid_signature", "Invalid payment notification signature")
		} else {
			billingError(w, err)
		}
		return
	}
	if r.PathValue("provider") == "alipay" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("success"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func checkoutHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
}

var checkoutTemplate = template.Must(template.New("checkout").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Agent Continuity 安全收银台</title><link rel="stylesheet" href="/styles.css"><body><main class="checkout-shell"><section class="card"><span class="eyebrow">AGENT CONTINUITY · CHECKOUT</span><h1>账单支付 / Invoice checkout</h1><p>{{.Number}}</p><h2>{{.Currency}} {{.Amount}}</h2><p>支付渠道：{{.Provider}} · 环境：{{.Environment}}</p><p>状态：{{.Status}}</p>{{if .Demo}}<p class="payment-warning">本地模拟演示，不会扣款。Simulation only: no money is moved.</p>{{end}}<p>链接有效期：{{.Expires}}</p>{{if .Pending}}{{if .Gateway}}<p><a href="{{.Gateway}}" rel="noreferrer noopener">前往官方收银台 / Continue to provider</a></p>{{end}}{{if .QR}}<img class="payment-qr" src="{{.QR}}" alt="支付二维码"><p>使用对应支付 App 扫码，支付金额以本页与官方收银台共同显示为准。</p>{{end}}<form method="post" action="{{.Confirm}}"><button name="outcome" value="succeeded">{{if .Demo}}模拟支付成功{{else}}查询并确认支付结果{{end}}</button>{{if .Demo}}<button name="outcome" value="failed">模拟支付失败</button>{{end}}</form>{{end}}{{if .Paid}}<p>支付已由服务端确认，付款回执可在管理控制台下载。</p>{{end}}<p class="muted">浏览器跳转不是付款凭证；只有渠道验签通知或服务端查单成功后才会入账。Payment receipt is not a tax invoice.</p></section></main></body></html>`))

func (s *Server) checkoutPage(w http.ResponseWriter, r *http.Request) {
	checkoutHeaders(w)
	d, p, err := s.billing.PublicPayment(r.Context(), r.PathValue("paymentID"), r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, "支付链接无效、已撤销或已过期 / Invalid or expired payment link", 404)
		return
	}
	link := s.billing.Link(p)
	suffix := strings.SplitN(link, "?", 2)[1]
	qr := ""
	if p.QRCode != "" {
		qr = "/pay/" + p.ID + "/qr?" + suffix
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = checkoutTemplate.Execute(w, map[string]any{"Number": d.Invoice.Number, "Currency": p.Currency, "Amount": payments.Decimal(p.AmountMinor), "Provider": p.Provider, "Environment": p.Environment, "Status": p.Status, "Demo": p.Provider == "demo", "Expires": p.ExpiresAt.Format(time.RFC3339), "Gateway": p.CheckoutURL, "QR": qr, "Confirm": "/pay/" + p.ID + "/confirm?" + suffix, "Pending": p.Status == "pending" || p.Status == "approved", "Paid": p.Status == "succeeded"})
}
func (s *Server) checkoutQR(w http.ResponseWriter, r *http.Request) {
	checkoutHeaders(w)
	_, p, err := s.billing.PublicPayment(r.Context(), r.PathValue("paymentID"), r.URL.Query().Get("token"))
	if err != nil || p.QRCode == "" {
		http.NotFound(w, r)
		return
	}
	svg, err := qrcode.SVG(p.QRCode)
	if err != nil {
		http.Error(w, "QR unavailable", 422)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(svg))
}
func (s *Server) confirmCheckout(w http.ResponseWriter, r *http.Request) {
	checkoutHeaders(w)
	d, p, err := s.billing.PublicPayment(r.Context(), r.PathValue("paymentID"), r.URL.Query().Get("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err = r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	if p.Provider == "demo" {
		_, err = s.billing.Simulate(r.Context(), d.Invoice.AccountID, d.Invoice.ID, p.ID, r.PostForm.Get("outcome"))
	} else {
		_, err = s.billing.Reconcile(r.Context(), d.Invoice.AccountID, d.Invoice.ID, p.ID, true)
	}
	if err != nil {
		billingError(w, err)
		return
	}
	http.Redirect(w, r, s.billing.Link(p), http.StatusSeeOther)
}

func csvSafe(v string) string {
	if strings.HasPrefix(v, "=") || strings.HasPrefix(v, "+") || strings.HasPrefix(v, "-") || strings.HasPrefix(v, "@") || strings.HasPrefix(v, "\t") || strings.HasPrefix(v, "\r") {
		return "'" + v
	}
	return v
}
func (s *Server) exportInvoice(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeInvoiceScope(w, r, "billing:read")
	if !ok {
		return
	}
	d, err := s.store.GetBill(r.Context(), claims.AccountID, r.PathValue("invoiceID"))
	if err != nil {
		billingError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+d.Invoice.Number+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	writer := csv.NewWriter(w)
	defer writer.Flush()
	_ = writer.Write([]string{"Invoice", "Currency", "Period start", "Period end", "Meter", "Description", "Quantity", "Unit", "Unit size", "Unit price (minor)", "Amount (minor)", "Price book", "Test mode", "Service tenant", "Liable tenant", "Payment mandate", "Issued by"})
	for _, l := range d.Invoice.Lines {
		_ = writer.Write([]string{d.Invoice.Number, d.Invoice.Currency, d.Invoice.PeriodStart.Format(time.RFC3339), d.Invoice.PeriodEnd.Format(time.RFC3339), csvSafe(l.Metric), csvSafe(l.Description), strconv.FormatInt(l.Quantity, 10), csvSafe(l.Unit), strconv.FormatInt(l.UnitSize, 10), strconv.FormatInt(l.UnitPriceMinor, 10), strconv.FormatInt(l.AmountMinor, 10), csvSafe(d.Invoice.PriceBook), strconv.FormatBool(d.Invoice.TestMode), csvSafe(d.Invoice.OrganizationID), csvSafe(d.Invoice.PayerOrganizationID), csvSafe(d.Invoice.PaymentMandateID), csvSafe(d.Invoice.IssuedBy)})
	}
}

var receiptTemplate = template.Must(template.New("receipt").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>支付回执 Payment receipt</title><link rel="stylesheet" href="/styles.css"><body><main class="checkout-shell"><section class="card"><h1>支付回执 / Payment receipt</h1><p>回执：{{.Receipt.ID}}</p><p>账单：{{.Invoice.Number}} · 当前状态：{{.Invoice.Status}}</p><h2>{{.Receipt.Currency}} {{.Amount}}</h2>{{if .Invoice.OrganizationID}}<p>服务租户：{{.Invoice.OrganizationID}}</p><p>付款责任租户：{{.Invoice.PayerOrganizationID}}</p><p>代付授权：{{.Invoice.PaymentMandateID}}</p>{{end}}{{if .PaidBy}}<p>发起付款账号：{{.PaidBy}}</p>{{end}}<p>渠道：{{.Receipt.Provider}} · 环境：{{.Receipt.Environment}}</p><p>支付单：{{.Receipt.PaymentID}}</p><p>渠道流水号：{{.Receipt.ProviderTransactionID}}</p><p>支付时间：{{.Receipt.PaidAt}}</p><p>此凭证仅为平台付款回执，不是税务发票。测试/模拟回执不代表真实资金到账。</p><p>This is a payment receipt, not a tax invoice. Test receipts do not represent settled funds.</p></section></main></body></html>`))

func (s *Server) getReceipt(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeInvoiceScope(w, r, "billing:read")
	if !ok {
		return
	}
	d, err := s.store.GetBill(r.Context(), claims.AccountID, r.PathValue("invoiceID"))
	if err != nil {
		billingError(w, err)
		return
	}
	for _, receipt := range d.Receipts {
		if receipt.ID == r.PathValue("receiptID") {
			checkoutHeaders(w)
			if r.URL.Query().Get("format") == "html" {
				paidBy := ""
				for _, p := range d.Payments {
					if p.ID == receipt.PaymentID {
						paidBy = p.InitiatedBy
					}
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.html"`, receipt.ID))
				_ = receiptTemplate.Execute(w, map[string]any{"Invoice": d.Invoice, "Receipt": receipt, "Amount": payments.Decimal(receipt.AmountMinor), "PaidBy": paidBy})
				return
			}
			writeJSON(w, 200, receipt)
			return
		}
	}
	billingError(w, billing.ErrNotFound)
}
