package payments

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMoneyAndConfigurationBoundaries(t *testing.T) {
	for _, n := range []int64{0, 1, 100, 199, 99999999999999} {
		v, err := ParseDecimal(Decimal(n))
		if err != nil || v != n {
			t.Fatalf("roundtrip %d -> %d %v", n, v, err)
		}
	}
	for _, value := range []string{"-1", "1.001", "1e4", "NaN", "+1", "1.", ".5", "1.2.3", "1234567890123.00"} {
		if _, err := ParseDecimal(value); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
	if _, err := Build(Config{DemoEnabled: true, PublicBaseURL: "http://127.0.0.1:18080"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Config{{DemoEnabled: true, PublicBaseURL: "http://example.com"}, {PublicBaseURL: "https://billing.example.com", Wechat: &WechatConfig{}}, {PublicBaseURL: "https://billing.example.com", PayPal: &PayPalConfig{}}, {DemoEnabled: true, PublicBaseURL: "https://user:pass@example.com"}} {
		if _, err := Build(c); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
}
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func TestAlipayProtocolAndNotificationVerification(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		v := r.PostForm
		if verifyRSA(&merchant.PublicKey, canonical(v, false), v.Get("sign")) != nil {
			t.Error("invalid outgoing RSA2 signature")
		}
		var biz map[string]any
		_ = json.Unmarshal([]byte(v.Get("biz_content")), &biz)
		if biz["out_trade_no"] != "pay_test" {
			t.Error("merchant order missing")
		}
		var body string
		switch v.Get("method") {
		case "alipay.trade.precreate":
			if biz["total_amount"] != "12.34" || biz["seller_id"] != "seller" {
				t.Error("amount or seller mismatch")
			}
			body = `{"code":"10000","out_trade_no":"pay_test","qr_code":"https://qr.alipay.com/test"}`
		case "alipay.trade.query":
			body = `{"code":"10000","out_trade_no":"pay_test","trade_no":"trade-123","trade_status":"TRADE_SUCCESS","total_amount":"12.34"}`
		default:
			t.Error("unexpected operation")
		}
		sig, _ := signRSA(platform, body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"%s_response":%s,"sign":%q}`, strings.ReplaceAll(v.Get("method"), ".", "_"), body, sig)
	}))
	defer server.Close()
	p := &Alipay{config: AlipayConfig{AppID: "app", SellerID: "seller", Sandbox: true}, key: merchant, public: &platform.PublicKey, http: server.Client(), base: server.URL}
	o := Order{PaymentID: "pay_test", AmountMinor: 1234, Currency: "CNY", NotifyURL: "https://example.com/notify"}
	r, err := p.Create(context.Background(), o)
	if err != nil || r.QRCode == "" {
		t.Fatalf("create: %+v %v", r, err)
	}
	r, err = p.Query(context.Background(), o)
	if err != nil || r.Status != "succeeded" || r.AmountMinor != 1234 {
		t.Fatalf("query: %+v %v", r, err)
	}
	v := url.Values{"app_id": {"app"}, "seller_id": {"seller"}, "out_trade_no": {"pay_test"}, "trade_no": {"trade-123"}, "trade_status": {"TRADE_SUCCESS"}, "total_amount": {"12.34"}, "notify_id": {"event-1"}, "sign_type": {"RSA2"}}
	sig, _ := signRSA(platform, canonical(v, true))
	v.Set("sign", sig)
	r, err = p.VerifyNotification(context.Background(), nil, []byte(v.Encode()))
	if err != nil || r.EventID != "event-1" {
		t.Fatalf("notification: %+v %v", r, err)
	}
	v.Set("total_amount", "0.01")
	if _, err = p.VerifyNotification(context.Background(), nil, []byte(v.Encode())); err == nil {
		t.Fatal("tampered notification accepted")
	}
	v.Set("seller_id", "other")
	sig, _ = signRSA(platform, canonical(v, true))
	v.Set("sign", sig)
	if _, err = p.VerifyNotification(context.Background(), nil, []byte(v.Encode())); err == nil {
		t.Fatal("wrong merchant accepted")
	}
	v.Add("total_amount", "12.34")
	if _, err = p.VerifyNotification(context.Background(), nil, []byte(v.Encode())); err == nil {
		t.Fatal("ambiguous fields accepted")
	}
}
func signWechat(t *testing.T, key *rsa.PrivateKey, h http.Header, body []byte) {
	t.Helper()
	h.Set("Wechatpay-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	h.Set("Wechatpay-Nonce", "test-nonce")
	h.Set("Wechatpay-Serial", "platform")
	sig, err := signRSA(key, h.Get("Wechatpay-Timestamp")+"\n"+h.Get("Wechatpay-Nonce")+"\n"+string(body)+"\n")
	if err != nil {
		t.Fatal(err)
	}
	h.Set("Wechatpay-Signature", sig)
}
func TestWechatResponseAndAEADNotification(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	apiKey := strings.Repeat("a", 32)
	transaction := []byte(`{"appid":"app","mchid":"merchant","out_trade_no":"pay_test","transaction_id":"wx-1","trade_state":"SUCCESS","amount":{"total":1234,"currency":"CNY"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		params := map[string]string{}
		for _, s := range strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "WECHATPAY2-SHA256-RSA2048 "), ",") {
			k, v, _ := strings.Cut(s, "=")
			params[k] = strings.Trim(v, `"`)
		}
		canonical := r.Method + "\n" + r.URL.RequestURI() + "\n" + params["timestamp"] + "\n" + params["nonce_str"] + "\n" + string(body) + "\n"
		if verifyRSA(&merchant.PublicKey, canonical, params["signature"]) != nil {
			t.Error("outgoing request signature invalid")
		}
		response := transaction
		if r.Method == "POST" {
			var v map[string]any
			_ = json.Unmarshal(body, &v)
			if v["out_trade_no"] != "pay_test" || v["mchid"] != "merchant" {
				t.Error("invalid create payload")
			}
			response = []byte(`{"code_url":"weixin://wxpay/test"}`)
		}
		signWechat(t, platform, w.Header(), response)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response)
	}))
	defer server.Close()
	p := &Wechat{config: WechatConfig{AppID: "app", MerchantID: "merchant", Serial: "merchant-serial", APIv3Key: apiKey}, key: merchant, platform: map[string]*rsa.PublicKey{"platform": &platform.PublicKey}, http: server.Client(), base: server.URL}
	o := Order{PaymentID: "pay_test", AmountMinor: 1234, Currency: "CNY", ExpiresAt: time.Now().Add(time.Hour)}
	if r, err := p.Create(context.Background(), o); err != nil || r.QRCode == "" {
		t.Fatalf("create %+v %v", r, err)
	}
	if r, err := p.Query(context.Background(), o); err != nil || r.TransactionID != "wx-1" {
		t.Fatalf("query %+v %v", r, err)
	}
	block, _ := aes.NewCipher([]byte(apiKey))
	aead, _ := cipher.NewGCM(block)
	nonce := []byte("123456789012")
	ct := aead.Seal(nil, nonce, transaction, []byte("transaction"))
	body, _ := json.Marshal(map[string]any{"id": "event-wx-1", "resource": map[string]string{"algorithm": "AEAD_AES_256_GCM", "nonce": string(nonce), "associated_data": "transaction", "ciphertext": base64.StdEncoding.EncodeToString(ct)}})
	h := http.Header{}
	signWechat(t, platform, h, body)
	r, err := p.VerifyNotification(context.Background(), h, body)
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("callback %+v %v", r, err)
	}
	if _, err = p.VerifyNotification(context.Background(), h, append(body, ' ')); err == nil {
		t.Fatal("tamper accepted")
	}
	h.Set("Wechatpay-Serial", "unknown")
	if _, err = p.VerifyNotification(context.Background(), h, body); err == nil {
		t.Fatal("unknown key accepted")
	}
	signWechat(t, platform, h, body)
	h.Set("Wechatpay-Timestamp", "1")
	if _, err = p.VerifyNotification(context.Background(), h, body); err == nil {
		t.Fatal("expired signature accepted")
	}
}
func TestPayPalOrderCaptureAndRemoteWebhookVerification(t *testing.T) {
	var captures atomic.Int32
	var tokens atomic.Int32
	order := func(status string) string {
		extra := ""
		if status == "COMPLETED" {
			extra = `,"payments":{"captures":[{"id":"capture-1","status":"COMPLETED","amount":{"currency_code":"USD","value":"12.34"}}]}`
		}
		return `{"id":"ORDER1","status":"` + status + `","purchase_units":[{"custom_id":"pay_test","payee":{"merchant_id":"MERCHANT"},"amount":{"currency_code":"USD","value":"12.34"}` + extra + `}],"links":[{"rel":"payer-action","href":"https://www.sandbox.paypal.com/checkoutnow?token=ORDER1"}]}`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/oauth2/token":
			id, secret, _ := r.BasicAuth()
			if id != "client" || secret != "secret" {
				t.Error("bad auth")
			}
			tokens.Add(1)
			fmt.Fprint(w, `{"access_token":"test-access","expires_in":3600}`)
		case "/v2/checkout/orders":
			if r.Header.Get("PayPal-Request-Id") != "pay_test" {
				t.Error("missing idempotency")
			}
			fmt.Fprint(w, order("CREATED"))
		case "/v2/checkout/orders/ORDER1":
			fmt.Fprint(w, order("APPROVED"))
		case "/v2/checkout/orders/ORDER1/capture":
			captures.Add(1)
			if r.Header.Get("PayPal-Request-Id") != "pay_test_cap" {
				t.Error("missing capture idempotency")
			}
			fmt.Fprint(w, order("COMPLETED"))
		case "/v1/notifications/verify-webhook-signature":
			var v map[string]any
			_ = json.NewDecoder(r.Body).Decode(&v)
			status := "FAILURE"
			if v["webhook_id"] == "WH1" && v["transmission_sig"] == "good" {
				status = "SUCCESS"
			}
			fmt.Fprintf(w, `{"verification_status":%q}`, status)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := &PayPal{config: PayPalConfig{ClientID: "client", ClientSecret: "secret", MerchantID: "MERCHANT", WebhookID: "WH1", Sandbox: true}, base: server.URL, http: server.Client()}
	o := Order{PaymentID: "pay_test", ProviderOrderID: "ORDER1", AmountMinor: 1234, Currency: "USD", ReturnURL: "https://example.com/pay"}
	if r, err := p.Create(context.Background(), o); err != nil || r.Status != "pending" {
		t.Fatalf("create %+v %v", r, err)
	}
	if r, err := p.Query(context.Background(), o); err != nil || r.Status != "approved" {
		t.Fatalf("query %+v %v", r, err)
	}
	if r, err := p.Capture(context.Background(), o); err != nil || r.Status != "succeeded" || r.TransactionID != "capture-1" {
		t.Fatalf("capture %+v %v", r, err)
	}
	if captures.Load() != 1 || tokens.Load() != 1 {
		t.Fatal("unexpected capture or token calls")
	}
	body := []byte(`{"id":"event1","event_type":"PAYMENT.CAPTURE.COMPLETED","resource":{"id":"capture-1","status":"COMPLETED","amount":{"currency_code":"USD","value":"12.34"},"payee":{"merchant_id":"MERCHANT"},"supplementary_data":{"related_ids":{"order_id":"ORDER1"}}}}`)
	h := http.Header{}
	for _, k := range []string{"Paypal-Auth-Algo", "Paypal-Cert-Url", "Paypal-Transmission-Id", "Paypal-Transmission-Sig", "Paypal-Transmission-Time"} {
		h.Set(k, "good")
	}
	if r, err := p.VerifyNotification(context.Background(), h, body); err != nil || r.OrderID != "ORDER1" || r.EventID != "event1" {
		t.Fatalf("verify %+v %v", r, err)
	}
	h.Set("Paypal-Transmission-Sig", "bad")
	if _, err := p.VerifyNotification(context.Background(), h, body); err == nil {
		t.Fatal("unverified webhook accepted")
	}
}
func TestStripeCheckoutAndWebhook(t *testing.T) {
	body := []byte(`{"id":"cs_test_1","object":"checkout.session","url":"https://checkout.stripe.com/test","client_reference_id":"pay_test","status":"complete","payment_status":"paid","payment_intent":"pi_test_1","currency":"usd","amount_total":1234,"livemode":false}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_test_secret" {
			t.Error("bad auth")
		}
		if r.Method == "POST" {
			_ = r.ParseForm()
			if r.Form.Get("line_items[0][price_data][unit_amount]") != "1234" || r.Header.Get("Idempotency-Key") != "pay_test" {
				t.Error("invalid create")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	p := &Stripe{config: StripeConfig{SecretKey: "sk_test_secret", WebhookSecret: "whsec_test", Sandbox: true}, base: server.URL, http: server.Client()}
	o := Order{PaymentID: "pay_test", ProviderOrderID: "cs_test_1", AmountMinor: 1234, Currency: "USD", ExpiresAt: time.Now().Add(time.Hour)}
	if r, err := p.Create(context.Background(), o); err != nil || r.Status != "succeeded" {
		t.Fatalf("create %+v %v", r, err)
	}
	if r, err := p.Query(context.Background(), o); err != nil || r.AmountMinor != 1234 {
		t.Fatalf("query %+v %v", r, err)
	}
	event := append([]byte(`{"id":"evt_1","type":"checkout.session.completed","livemode":false,"data":{"object":`), body...)
	event = append(event, []byte(`}}`)...)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write([]byte(ts + "."))
	mac.Write(event)
	h := http.Header{"Stripe-Signature": {"t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))}}
	if r, err := p.VerifyNotification(context.Background(), h, event); err != nil || r.EventID != "evt_1" {
		t.Fatalf("webhook %+v %v", r, err)
	}
	if _, err := p.VerifyNotification(context.Background(), h, bytes.ReplaceAll(event, []byte("1234"), []byte("9999"))); err == nil {
		t.Fatal("tampered webhook accepted")
	}
}
