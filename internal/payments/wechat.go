package payments

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Wechat struct {
	config   WechatConfig
	key      *rsa.PrivateKey
	platform map[string]*rsa.PublicKey
	http     *http.Client
	base     string
}

func newWechat(c WechatConfig) (*Wechat, error) {
	if c.AppID == "" || c.MerchantID == "" || c.Serial == "" || len(c.APIv3Key) != 32 || len(c.PlatformKeyFiles) == 0 {
		return nil, errors.New("WeChat requires app_id, merchant_id, certificate_serial, 32-byte APIv3 key and platform verification keys")
	}
	b, err := readKey(c.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	key, err := privateKey(b)
	if err != nil {
		return nil, err
	}
	p := &Wechat{config: c, key: key, platform: map[string]*rsa.PublicKey{}, http: client(), base: "https://api.mch.weixin.qq.com"}
	for serial, path := range c.PlatformKeyFiles {
		b, err = readKey(path)
		if err != nil {
			return nil, err
		}
		p.platform[serial], err = publicKey(b)
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}
func (p *Wechat) Info() Info {
	return Info{ID: "wechat", Name: "微信支付 Native", Environment: "live", Enabled: true, Currencies: []string{"CNY"}, Capabilities: []string{"qr_checkout", "query", "signed_webhook"}}
}
func (p *Wechat) verify(h http.Header, b []byte) error {
	ts, err := strconv.ParseInt(h.Get("Wechatpay-Timestamp"), 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute {
		return ErrSignature
	}
	if h.Get("Wechatpay-Nonce") == "" {
		return ErrSignature
	}
	return verifyRSA(p.platform[h.Get("Wechatpay-Serial")], fmt.Sprintf("%s\n%s\n%s\n", h.Get("Wechatpay-Timestamp"), h.Get("Wechatpay-Nonce"), b), h.Get("Wechatpay-Signature"))
}
func (p *Wechat) call(ctx context.Context, method, path string, input any) ([]byte, error) {
	var b []byte
	var err error
	if input != nil {
		b, err = json.Marshal(input)
		if err != nil {
			return nil, err
		}
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := NewID("")
	sig, err := signRSA(p.key, method+"\n"+path+"\n"+ts+"\n"+nonce+"\n"+string(b)+"\n")
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, method, p.base+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`, p.config.MerchantID, nonce, ts, p.config.Serial, sig))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	resp, err := p.http.Do(r)
	if err != nil {
		return nil, err
	}
	h := resp.Header.Clone()
	b, err = readResponse(resp)
	if err != nil {
		return nil, err
	}
	if err = p.verify(h, b); err != nil {
		return nil, err
	}
	return b, nil
}
func (p *Wechat) Create(ctx context.Context, o Order) (Result, error) {
	b, err := p.call(ctx, "POST", "/v3/pay/transactions/native", map[string]any{"appid": p.config.AppID, "mchid": p.config.MerchantID, "description": o.Description, "out_trade_no": o.PaymentID, "notify_url": o.NotifyURL, "time_expire": o.ExpiresAt.Format(time.RFC3339), "amount": map[string]any{"total": o.AmountMinor, "currency": o.Currency}})
	if err != nil {
		return Result{}, err
	}
	var v struct {
		CodeURL string `json:"code_url"`
	}
	if err = json.Unmarshal(b, &v); err != nil || v.CodeURL == "" {
		return Result{}, errors.New("WeChat missing code_url")
	}
	return Result{PaymentID: o.PaymentID, OrderID: o.PaymentID, Status: "pending", QRCode: v.CodeURL, AmountMinor: o.AmountMinor, Currency: o.Currency}, nil
}
func (p *Wechat) Query(ctx context.Context, o Order) (Result, error) {
	b, err := p.call(ctx, "GET", "/v3/pay/transactions/out-trade-no/"+url.PathEscape(o.PaymentID)+"?mchid="+url.QueryEscape(p.config.MerchantID), nil)
	if err != nil {
		return Result{}, err
	}
	return p.transaction(b)
}
func (p *Wechat) Capture(context.Context, Order) (Result, error) { return Result{}, ErrUnsupported }
func (p *Wechat) transaction(b []byte) (Result, error) {
	var v struct {
		AppID       string    `json:"appid"`
		MerchantID  string    `json:"mchid"`
		TradeNo     string    `json:"out_trade_no"`
		Transaction string    `json:"transaction_id"`
		Status      string    `json:"trade_state"`
		SuccessTime time.Time `json:"success_time"`
		Amount      struct {
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return Result{}, err
	}
	if v.AppID != p.config.AppID || v.MerchantID != p.config.MerchantID {
		return Result{}, errors.New("WeChat merchant/app mismatch")
	}
	status := "pending"
	switch v.Status {
	case "SUCCESS":
		status = "succeeded"
	case "CLOSED", "REVOKED":
		status = "canceled"
	case "PAYERROR":
		status = "failed"
	case "REFUND":
		status = "review" // REFUND means a refund process exists, not proof of a full refund.
	}
	return Result{PaymentID: v.TradeNo, OrderID: v.TradeNo, TransactionID: v.Transaction, Status: status, AmountMinor: v.Amount.Total, Currency: v.Amount.Currency, PaidAt: v.SuccessTime}, nil
}
func (p *Wechat) VerifyNotification(_ context.Context, h http.Header, b []byte) (Result, error) {
	if err := p.verify(h, b); err != nil {
		return Result{}, err
	}
	var v struct {
		ID       string `json:"id"`
		Resource struct {
			Algorithm      string `json:"algorithm"`
			Ciphertext     string `json:"ciphertext"`
			Nonce          string `json:"nonce"`
			AssociatedData string `json:"associated_data"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return Result{}, err
	}
	if v.Resource.Algorithm != "AEAD_AES_256_GCM" {
		return Result{}, ErrSignature
	}
	c, err := aes.NewCipher([]byte(p.config.APIv3Key))
	if err != nil {
		return Result{}, err
	}
	gcm, err := cipher.NewGCM(c)
	if err != nil {
		return Result{}, err
	}
	ct, err := base64.StdEncoding.DecodeString(v.Resource.Ciphertext)
	if err != nil || len(v.Resource.Nonce) != gcm.NonceSize() {
		return Result{}, ErrSignature
	}
	plain, err := gcm.Open(nil, []byte(v.Resource.Nonce), ct, []byte(v.Resource.AssociatedData))
	if err != nil {
		return Result{}, ErrSignature
	}
	result, err := p.transaction(plain)
	result.EventID = v.ID
	return result, err
}
