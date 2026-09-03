package httpapi

import (
	"encoding/csv"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func WithTreasury(owner string, demo bool, stripe *treasury.Stripe) Option {
	return func(s *Server) { s.treasury = treasury.New(s.store, owner, demo, stripe) }
}
func (s *Server) treasuryAuth(w http.ResponseWriter, r *http.Request, scope string) (auth.Claims, bool) {
	c, ok := s.authorizeScope(w, r, scope)
	if !ok {
		return c, false
	}
	if s.treasury == nil || s.treasury.Owner == "" {
		writeError(w, 503, "treasury_unconfigured", "收款与提现尚未配置运营方账户 / Treasury owner is not configured")
		return c, false
	}
	if c.AccountID != s.treasury.Owner {
		writeError(w, 403, "treasury_forbidden", "仅指定收款运营方可访问；付款账户不是商户资金账户 / Operator-only treasury")
		return c, false
	}
	return c, true
}
func treasuryError(w http.ResponseWriter, err error) {
	status := 502
	switch {
	case errors.Is(err, treasury.ErrNotFound):
		status = 404
	case errors.Is(err, treasury.ErrConflict), errors.Is(err, treasury.ErrInsufficient):
		status = 409
	case errors.Is(err, treasury.ErrInvalid):
		status = 422
	case errors.Is(err, treasury.ErrUnavailable):
		status = 503
	}
	writeError(w, status, "treasury_error", err.Error())
}
func treasuryResult(w http.ResponseWriter, d protocol.TreasuryDocument, err error) {
	if err != nil {
		treasuryError(w, err)
		return
	}
	writeJSON(w, 200, d)
}
func (s *Server) registerTreasuryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/treasury/receivables", s.treasuryReceivables)
	mux.HandleFunc("GET /v1/treasury/receivables/{invoiceID}", s.getInvoice)
	mux.HandleFunc("GET /v1/treasury/receivables/{invoiceID}/export", s.exportInvoice)
	mux.HandleFunc("GET /v1/treasury/receivables/{invoiceID}/receipts/{receiptID}", s.getReceipt)
	mux.HandleFunc("GET /v1/treasury", s.getTreasury)
	mux.HandleFunc("POST /v1/treasury/collections/import", s.importTreasuryCollection)
	mux.HandleFunc("POST /v1/treasury/collections/{collectionID}/settle-demo", s.settleTreasuryCollection)
	mux.HandleFunc("POST /v1/treasury/destinations", s.addTreasuryDestination)
	mux.HandleFunc("POST /v1/treasury/destinations/{destinationID}/verify-demo", s.verifyTreasuryDestination)
	mux.HandleFunc("GET /v1/treasury/stripe/snapshot", s.treasuryStripeSnapshot)
	mux.HandleFunc("POST /v1/treasury/payouts", s.requestTreasuryPayout)
	mux.HandleFunc("POST /v1/treasury/payouts/{payoutID}/decision", s.decideTreasuryPayout)
	mux.HandleFunc("POST /v1/treasury/payouts/{payoutID}/submit", s.submitTreasuryPayout)
	mux.HandleFunc("POST /v1/treasury/payouts/{payoutID}/reconcile", s.reconcileTreasuryPayout)
	mux.HandleFunc("POST /v1/treasury/payouts/{payoutID}/simulate", s.simulateTreasuryPayout)
	mux.HandleFunc("GET /v1/treasury/payouts/{payoutID}/receipt", s.treasuryReceipt)
	mux.HandleFunc("GET /v1/treasury/export", s.exportTreasury)
}
func (s *Server) treasuryReceivables(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.treasuryAuth(w, r, "treasury:read"); !ok {
		return
	}
	docs, err := s.store.ListMerchantBills(r.Context(), 200)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	for i := range docs {
		hideCheckout(&docs[i])
	}
	writeJSON(w, 200, map[string]any{"invoices": docs, "limit": 200, "read_only": true})
}
func (s *Server) getTreasury(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.treasuryAuth(w, r, "treasury:read"); !ok {
		return
	}
	d, err := s.treasury.Load(r.Context())
	if err != nil {
		treasuryError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"document": d, "balances": treasury.Balances(d), "demo_enabled": s.treasury.Demo, "stripe_api_enabled": s.treasury.Stripe != nil, "routes": []map[string]string{
		{"provider": "demo", "method": "simulation", "destination": "bank / Alipay / PayPal virtual own accounts", "note": "完整状态模拟，不扣款、不产生真实银行流水"},
		{"provider": "wechat", "method": "channel_dashboard", "destination": "商户绑定的结算银行账户", "url": "https://pay.weixin.qq.com/", "note": "普通商户按签约规则结算；不把服务商专用提现 API 冒充普通商户 API"},
		{"provider": "alipay", "method": "channel_dashboard", "destination": "商家支付宝账户 → 绑定银行账户", "url": "https://b.alipay.com/", "note": "真实结算/提现在支付宝商家平台完成；未接自动转账接口"},
		{"provider": "paypal", "method": "channel_dashboard", "destination": "PayPal 余额 → 符合地区要求的银行账户", "url": "https://www.paypal.com/myaccount/money/", "note": "银行提现在 PayPal 完成；Payouts 向其他钱包付款不等于自己提现"},
		{"provider": "stripe", "method": "optional_payout_api", "destination": "当前商户的默认结算银行账户", "url": "https://dashboard.stripe.com/balance/overview", "note": "配置后可查询余额、审批、提交和查单；默认关闭，未进行真实商户验收"},
	}})
}
func (s *Server) importTreasuryCollection(w http.ResponseWriter, r *http.Request) {
	c, ok := s.treasuryAuth(w, r, "treasury:write")
	if !ok {
		return
	}
	var in struct {
		InvoiceID string `json:"invoice_id"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	d, err := s.treasury.ImportDemo(r.Context(), in.InvoiceID, c.Subject)
	treasuryResult(w, d, err)
}
func (s *Server) settleTreasuryCollection(w http.ResponseWriter, r *http.Request) {
	c, ok := s.treasuryAuth(w, r, "treasury:write")
	if !ok {
		return
	}
	d, err := s.treasury.SettleDemo(r.Context(), r.PathValue("collectionID"), c.Subject)
	treasuryResult(w, d, err)
}
func (s *Server) addTreasuryDestination(w http.ResponseWriter, r *http.Request) {
	c, ok := s.treasuryAuth(w, r, "treasury:write")
	if !ok {
		return
	}
	var in struct {
		Kind     string `json:"kind"`
		Label    string `json:"label"`
		Currency string `json:"currency"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	d, err := s.treasury.AddDemoDestination(r.Context(), in.Kind, in.Label, in.Currency, c.Subject)
	treasuryResult(w, d, err)
}
func (s *Server) verifyTreasuryDestination(w http.ResponseWriter, r *http.Request) {
	c, ok := s.treasuryAuth(w, r, "treasury:approve")
	if !ok {
		return
	}
	d, err := s.treasury.VerifyDemoDestination(r.Context(), r.PathValue("destinationID"), c.Subject)
	treasuryResult(w, d, err)
}
func (s *Server) treasuryStripeSnapshot(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.treasuryAuth(w, r, "treasury:read"); !ok {
		return
	}
	if s.treasury.Stripe == nil {
		treasuryError(w, treasury.ErrUnavailable)
		return
	}
	v, err := s.treasury.Stripe.Snapshot(r.Context(), r.URL.Query().Get("currency"))
	if err != nil {
		treasuryError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) requestTreasuryPayout(w http.ResponseWriter, r *http.Request) {
	c, ok := s.treasuryAuth(w, r, "treasury:write")
	if !ok {
		return
	}
	var in treasury.Request
	if !s.decodeJSON(w, r, &in) {
		return
	}
	d, err := s.treasury.RequestPayout(r.Context(), in, r.Header.Get("Idempotency-Key"), c.Subject)
	treasuryResult(w, d, err)
}
func (s *Server) decideTreasuryPayout(w http.ResponseWriter, r *http.Request) {
	c, ok := s.treasuryAuth(w, r, "treasury:approve")
	if !ok {
		return
	}
	var in struct {
		Decision string `json:"decision"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	d, err := s.treasury.Decide(r.Context(), r.PathValue("payoutID"), in.Decision, c.Subject)
	treasuryResult(w, d, err)
}
func (s *Server) submitTreasuryPayout(w http.ResponseWriter, r *http.Request) {
	c, ok := s.treasuryAuth(w, r, "treasury:approve")
	if !ok {
		return
	}
	d, err := s.treasury.Submit(r.Context(), r.PathValue("payoutID"), c.Subject)
	treasuryResult(w, d, err)
}
func (s *Server) reconcileTreasuryPayout(w http.ResponseWriter, r *http.Request) {
	c, ok := s.treasuryAuth(w, r, "treasury:write")
	if !ok {
		return
	}
	d, err := s.treasury.Reconcile(r.Context(), r.PathValue("payoutID"), c.Subject)
	treasuryResult(w, d, err)
}
func (s *Server) simulateTreasuryPayout(w http.ResponseWriter, r *http.Request) {
	c, ok := s.treasuryAuth(w, r, "treasury:write")
	if !ok {
		return
	}
	var in struct {
		Outcome string `json:"outcome"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	d, err := s.treasury.Simulate(r.Context(), r.PathValue("payoutID"), in.Outcome, c.Subject)
	treasuryResult(w, d, err)
}

var payoutReceiptTemplate = template.Must(template.New("payout-receipt").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>转出回单</title><body><h1>转出回单 / Payout record</h1><p>{{.Warning}}</p><p>回单编号：{{.P.ReceiptID}}</p><p>转出单：{{.P.ID}}</p><h2>{{.P.Currency}} {{.Amount}}</h2><p>收款账户：{{.P.Destination.Label}} · {{.P.Destination.MaskedAccount}}</p><p>渠道：{{.P.Provider}} · {{.P.Environment}}</p><p>渠道流水：{{.P.ProviderPayoutID}}</p><p>当前状态：{{if eq .P.Status "returned"}}到账后退回{{else}}渠道确认到账{{end}}（{{.P.Status}}）</p><p>渠道确认时间：{{if .P.PaidAt}}{{.P.PaidAt.Format "2006-01-02T15:04:05Z07:00"}}{{else}}—{{end}}</p><p>退回时间：{{if .P.ReturnedAt}}{{.P.ReturnedAt.Format "2006-01-02T15:04:05Z07:00"}}{{else}}—{{end}}</p><p>该记录不是税务发票。已到账后仍可能发生银行退回，应结合最新渠道状态及银行流水核对。</p></body></html>`))

func (s *Server) treasuryReceipt(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.treasuryAuth(w, r, "treasury:read"); !ok {
		return
	}
	d, err := s.treasury.Load(r.Context())
	if err != nil {
		treasuryError(w, err)
		return
	}
	for _, p := range d.Payouts {
		if p.ID != r.PathValue("payoutID") || p.ReceiptID == "" {
			continue
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.html"`, p.ReceiptID))
		warning := "渠道 API 状态记录，不替代银行到账证明。"
		if p.Environment != "live" {
			warning = "模拟 / 沙箱回单：未发生真实转账，不是银行凭证。"
		}
		_ = payoutReceiptTemplate.Execute(w, map[string]any{"P": p, "Amount": payments.Decimal(p.AmountMinor), "Warning": warning})
		return
	}
	treasuryError(w, treasury.ErrNotFound)
}
func (s *Server) exportTreasury(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.treasuryAuth(w, r, "treasury:read"); !ok {
		return
	}
	d, err := s.treasury.Load(r.Context())
	if err != nil {
		treasuryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="treasury-ledger.csv"`)
	out := csv.NewWriter(w)
	defer out.Flush()
	_ = out.Write([]string{"Type", "ID", "Currency", "Gross minor", "Fee minor", "Net/amount minor", "Status", "Environment", "Destination", "Provider reference", "Time"})
	for _, c := range d.Collections {
		_ = out.Write([]string{"collection", c.ID, c.Currency, strconv.FormatInt(c.GrossMinor, 10), strconv.FormatInt(c.FeeMinor, 10), strconv.FormatInt(c.NetMinor, 10), c.Status, c.Environment, "", csvSafe(c.ProviderTransactionID), c.ReceivedAt.Format(time.RFC3339)})
	}
	for _, p := range d.Payouts {
		_ = out.Write([]string{"payout", p.ID, p.Currency, "", "", strconv.FormatInt(p.AmountMinor, 10), p.Status, p.Environment, csvSafe(p.Destination.Label + " " + p.Destination.MaskedAccount), csvSafe(p.ProviderPayoutID), p.CreatedAt.Format(time.RFC3339)})
	}
}
