package payments

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Each HTTP substitute speaks the real gateway wire contract. Keys are generated
// for the test; no merchant credentials or live API endpoints are used.
func TestRefundGatewayContracts(t *testing.T) {
	for _, name := range []string{"wechat", "alipay", "paypal", "stripe"} {
		t.Run(name, func(t *testing.T) {
			merchant, platform := testKey(t), testKey(t)
			calls := 0
			ctx := context.Background()
			o := RefundOrder{RefundID: "rf_test", PaymentID: "pay_test", TransactionID: "transaction", Currency: "CNY", AmountMinor: 300, TotalMinor: 1000, Reason: "partial"}
			if name == "paypal" {
				o.Currency = "USD"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body string
				switch name {
				case "wechat":
					if !strings.HasPrefix(r.Header.Get("Authorization"), "WECHATPAY2-SHA256-RSA2048 ") {
						t.Error("missing authorization")
					}
					if r.Method == "POST" {
						var in map[string]any
						_ = json.NewDecoder(r.Body).Decode(&in)
						if in["out_refund_no"] != o.RefundID || in["transaction_id"] != o.TransactionID {
							t.Error("refund identity")
						}
					}
					if r.URL.Path != "/v3/refund/domestic/refunds" && r.URL.Path != "/v3/refund/domestic/refunds/rf_test" {
						t.Error(r.URL.Path)
					}
					body = `{"refund_id":"remote_rf","out_refund_no":"rf_test","transaction_id":"transaction","status":"SUCCESS","amount":{"refund":300,"total":1000,"currency":"CNY"}}`
					signWechat(t, platform, w.Header(), []byte(body))
				case "alipay":
					_ = r.ParseForm()
					if verifyRSA(&merchant.PublicKey, canonical(r.PostForm, false), r.PostForm.Get("sign")) != nil {
						t.Error("invalid RSA2")
					}
					var biz map[string]string
					_ = json.Unmarshal([]byte(r.PostForm.Get("biz_content")), &biz)
					if biz["out_request_no"] != o.RefundID || biz["trade_no"] != o.TransactionID {
						t.Error("refund identity")
					}
					method := r.PostForm.Get("method")
					if method != "alipay.trade.refund" && method != "alipay.trade.fastpay.refund.query" {
						t.Error(method)
					}
					b := `{"code":"10000","trade_no":"transaction","out_request_no":"rf_test","refund_amount":"3.00","refund_status":"REFUND_SUCCESS"}`
					sig, _ := signRSA(platform, b)
					body = fmt.Sprintf(`{"%s_response":%s,"sign":%q}`, strings.ReplaceAll(method, ".", "_"), b, sig)
				case "stripe":
					if r.Header.Get("Authorization") != "Bearer test-key" {
						t.Error("authorization")
					}
					if r.Method == "POST" {
						_ = r.ParseForm()
						if r.Header.Get("Idempotency-Key") != o.RefundID || r.PostForm.Get("amount") != "300" || r.PostForm.Get("payment_intent") != o.TransactionID {
							t.Error("stripe refund contract")
						}
					}
					if r.URL.Path != "/v1/refunds" && r.URL.Path != "/v1/refunds/remote_rf" {
						t.Error(r.URL.Path)
					}
					body = `{"id":"remote_rf","payment_intent":"transaction","metadata":{"refund_id":"rf_test"},"status":"succeeded","amount":300,"currency":"cny"}`
				case "paypal":
					if r.URL.Path == "/v1/oauth2/token" {
						fmt.Fprint(w, `{"access_token":"test-token","expires_in":3600}`)
						return
					}
					if r.Header.Get("Authorization") != "Bearer test-token" {
						t.Error("token")
					}
					if r.Method == "POST" && r.Header.Get("PayPal-Request-Id") != o.RefundID {
						t.Error("idempotency")
					}
					if r.URL.Path != "/v2/payments/captures/transaction/refund" && r.URL.Path != "/v2/payments/refunds/remote_rf" {
						t.Error(r.URL.Path)
					}
					body = `{"id":"remote_rf","invoice_id":"rf_test","status":"COMPLETED","amount":{"value":"3.00","currency_code":"USD"}}`
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			var p RefundProvider
			switch name {
			case "wechat":
				p = &Wechat{config: WechatConfig{MerchantID: "merchant", Serial: "serial"}, key: merchant, platform: map[string]*rsa.PublicKey{"platform": &platform.PublicKey}, http: server.Client(), base: server.URL}
			case "alipay":
				p = &Alipay{config: AlipayConfig{AppID: "app", SellerID: "seller"}, key: merchant, public: &platform.PublicKey, http: server.Client(), base: server.URL}
			case "stripe":
				p = &Stripe{config: StripeConfig{SecretKey: "test-key", Sandbox: true}, http: server.Client(), base: server.URL}
			case "paypal":
				p = &PayPal{config: PayPalConfig{ClientID: "test", ClientSecret: "test"}, http: server.Client(), base: server.URL}
			}
			got, err := p.Refund(ctx, o)
			if err != nil || got.Status != "succeeded" || got.AmountMinor != 300 {
				t.Fatalf("create %+v %v", got, err)
			}
			o.ProviderRefundID = got.ProviderRefundID
			got, err = p.QueryRefund(ctx, o)
			if err != nil || got.Status != "succeeded" {
				t.Fatalf("query %+v %v", got, err)
			}
			o.AmountMinor = 301
			if _, err = p.QueryRefund(ctx, o); err == nil {
				t.Fatal("tampered amount accepted")
			}
			if calls < 3 {
				t.Fatal("no wire requests")
			}
		})
	}
}
