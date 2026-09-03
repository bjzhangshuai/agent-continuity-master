package payments

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Alipay struct {
	config AlipayConfig
	key    *rsa.PrivateKey
	public *rsa.PublicKey
	http   *http.Client
	base   string
}

func newAlipay(c AlipayConfig) (*Alipay, error) {
	if c.AppID == "" || c.SellerID == "" {
		return nil, errors.New("Alipay requires app_id and seller_id")
	}
	b, err := readKey(c.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	key, err := privateKey(b)
	if err != nil {
		return nil, err
	}
	b, err = readKey(c.PublicKeyFile)
	if err != nil {
		return nil, err
	}
	pub, err := publicKey(b)
	if err != nil {
		return nil, err
	}
	base := "https://openapi.alipay.com/gateway.do"
	if c.Sandbox {
		base = "https://openapi-sandbox.dl.alipaydev.com/gateway.do"
	}
	return &Alipay{config: c, key: key, public: pub, http: client(), base: base}, nil
}
func (p *Alipay) Info() Info {
	return Info{ID: "alipay", Name: "支付宝 / Alipay 当面付", Environment: environment(p.config.Sandbox), Enabled: true, Currencies: []string{"CNY"}, Capabilities: []string{"qr_checkout", "query", "signed_webhook"}}
}
func canonical(v url.Values, notification bool) string {
	keys := []string{}
	for k := range v {
		if k != "sign" && (!notification || k != "sign_type") && v.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := []string{}
	for _, k := range keys {
		parts = append(parts, k+"="+v.Get(k))
	}
	return strings.Join(parts, "&")
}
func (p *Alipay) call(ctx context.Context, method string, biz any, notify string) ([]byte, error) {
	b, err := json.Marshal(biz)
	if err != nil {
		return nil, err
	}
	v := url.Values{"app_id": {p.config.AppID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"}, "sign_type": {"RSA2"}, "timestamp": {time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}, "version": {"1.0"}, "biz_content": {string(b)}}
	if notify != "" {
		v.Set("notify_url", notify)
	}
	sig, err := signRSA(p.key, canonical(v, false))
	if err != nil {
		return nil, err
	}
	v.Set("sign", sig)
	r, err := http.NewRequestWithContext(ctx, "POST", p.base, strings.NewReader(v.Encode()))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.http.Do(r)
	if err != nil {
		return nil, err
	}
	b, err = readResponse(resp)
	if err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(b, &envelope); err != nil {
		return nil, err
	}
	body := envelope[strings.ReplaceAll(method, ".", "_")+"_response"]
	var signature string
	if json.Unmarshal(envelope["sign"], &signature) != nil || verifyRSA(p.public, string(body), signature) != nil {
		return nil, ErrSignature
	}
	var status struct {
		Code    string `json:"code"`
		SubCode string `json:"sub_code"`
	}
	if err = json.Unmarshal(body, &status); err != nil {
		return nil, err
	}
	if status.Code != "10000" {
		if status.SubCode == "ACQ.TRADE_NOT_EXIST" {
			return nil, ErrNotFound
		}
		return nil, errors.New("Alipay rejected operation: " + status.Code)
	}
	return body, nil
}
func (p *Alipay) Create(ctx context.Context, o Order) (Result, error) {
	b, err := p.call(ctx, "alipay.trade.precreate", map[string]any{"out_trade_no": o.PaymentID, "total_amount": Decimal(o.AmountMinor), "subject": o.Description, "seller_id": p.config.SellerID, "timeout_express": "30m"}, o.NotifyURL)
	if err != nil {
		return Result{}, err
	}
	var v struct {
		TradeNo string `json:"out_trade_no"`
		QRCode  string `json:"qr_code"`
	}
	if json.Unmarshal(b, &v) != nil || v.QRCode == "" || v.TradeNo != o.PaymentID {
		return Result{}, errors.New("Alipay invalid checkout response")
	}
	return Result{PaymentID: o.PaymentID, OrderID: o.PaymentID, Status: "pending", QRCode: v.QRCode, AmountMinor: o.AmountMinor, Currency: o.Currency}, nil
}
func (p *Alipay) Query(ctx context.Context, o Order) (Result, error) {
	b, err := p.call(ctx, "alipay.trade.query", map[string]string{"out_trade_no": o.PaymentID}, "")
	if err != nil {
		return Result{}, err
	}
	var v struct {
		TradeNo     string      `json:"out_trade_no"`
		Transaction string      `json:"trade_no"`
		Status      string      `json:"trade_status"`
		Amount      json.Number `json:"total_amount"`
	}
	if err = json.Unmarshal(b, &v); err != nil {
		return Result{}, err
	}
	n, err := ParseDecimal(v.Amount.String())
	if err != nil {
		return Result{}, err
	}
	return alipayResult(v.TradeNo, v.Transaction, v.Status, n), nil
}
func alipayResult(id, transaction, status string, amount int64) Result {
	s := "pending"
	switch status {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		s = "succeeded"
	case "TRADE_CLOSED":
		s = "canceled"
	}
	return Result{PaymentID: id, OrderID: id, TransactionID: transaction, Status: s, AmountMinor: amount, Currency: "CNY"}
}
func (p *Alipay) Capture(context.Context, Order) (Result, error) { return Result{}, ErrUnsupported }
func (p *Alipay) VerifyNotification(_ context.Context, _ http.Header, b []byte) (Result, error) {
	v, err := url.ParseQuery(string(b))
	if err != nil {
		return Result{}, ErrSignature
	}
	for _, values := range v {
		if len(values) != 1 {
			return Result{}, ErrSignature
		}
	}
	if v.Get("sign_type") != "RSA2" || verifyRSA(p.public, canonical(v, true), v.Get("sign")) != nil {
		return Result{}, ErrSignature
	}
	if v.Get("app_id") != p.config.AppID || v.Get("seller_id") != p.config.SellerID {
		return Result{}, errors.New("Alipay merchant/app mismatch")
	}
	n, err := ParseDecimal(v.Get("total_amount"))
	if err != nil {
		return Result{}, err
	}
	r := alipayResult(v.Get("out_trade_no"), v.Get("trade_no"), v.Get("trade_status"), n)
	r.EventID = v.Get("notify_id")
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", v.Get("gmt_payment"), time.FixedZone("CST", 8*3600)); err == nil {
		r.PaidAt = t
	}
	return r, nil
}
