package treasury

import (
	"context"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStripePayoutProtocol(t *testing.T) {
	state := "pending"
	bad := false
	creates := 0
	bankDisabled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _, ok := r.BasicAuth()
		if !ok || u != "sk_test_fixture" {
			t.Error("authorization missing")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/account":
			if bankDisabled {
				fmt.Fprint(w, `{"id":"acct_owner","payouts_enabled":false,"external_accounts":{"data":[]}}`)
				return
			}
			fmt.Fprint(w, `{"id":"acct_owner","payouts_enabled":true,"settings":{"payouts":{"schedule":{"interval":"manual"}}},"external_accounts":{"data":[{"id":"ba_own","object":"bank_account","currency":"usd","last4":"1234","bank_name":"Test bank","default_for_currency":true,"status":"verified"}]}}`)
		case "/v1/balance":
			fmt.Fprint(w, `{"livemode":false,"available":[{"currency":"usd","amount":5000}],"pending":[{"currency":"usd","amount":1000}]}`)
		case "/v1/payouts", "/v1/payouts/po_provider":
			if r.Method == "POST" {
				creates++
				_ = r.ParseForm()
				if r.Header.Get("Idempotency-Key") != "po_request" || r.Form.Get("destination") != "ba_own" || r.Form.Get("amount") != "1000" {
					t.Error("mutable payout request")
				}
			}
			dest := "ba_own"
			if bad {
				dest = "ba_other"
			}
			fmt.Fprintf(w, `{"id":"po_provider","type":"bank_account","status":%q,"amount":1000,"currency":"usd","destination":%q,"livemode":false,"metadata":{"treasury_payout_id":"po_request"}}`, state, dest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := &Stripe{account: "acct_owner", key: "sk_test_fixture", sandbox: true, base: server.URL, http: server.Client()}
	ctx := context.Background()
	snap, err := p.Snapshot(ctx, "USD")
	if err != nil || snap.AvailableMinor != 5000 || snap.Destination.ID != "ba_own" {
		t.Fatal(snap, err)
	}
	o := protocol.TreasuryPayout{ID: "po_request", AmountMinor: 1000, Currency: "USD", Environment: "sandbox", Destination: snap.Destination}
	res, err := p.Create(ctx, o)
	if err != nil || res.Status != "processing" || creates != 1 {
		t.Fatal(res, err)
	}
	o.ProviderPayoutID = res.ID
	state = "paid"
	res, err = p.Query(ctx, o)
	if err != nil || res.Status != "paid" {
		t.Fatal(res, err)
	}
	bad = true
	if _, err = p.Query(ctx, o); err == nil {
		t.Fatal("foreign destination accepted")
	}
	bad = false
	bankDisabled = true
	state = "failed"
	if res, err = p.Query(ctx, o); err != nil || res.Status != "failed" {
		t.Fatal("cannot reconcile a rejected bank", res, err)
	}
	if _, err = p.Snapshot(ctx, "USD"); err == nil {
		t.Fatal("disabled bank accepted for new payouts")
	}
	p.sandbox = false
	if _, err = p.Query(ctx, o); err == nil {
		t.Fatal("environment switch accepted")
	}
}
func TestTreasuryConfigFailsClosed(t *testing.T) {
	for _, raw := range []string{`{"demo_enabled":true}`, `{"owner_account_id":"operator","allow_live":false,"stripe":{"account_id":"acct_x","secret_key_file":"/secret","sandbox":false}}`, `{"unknown":true}`, `{} {}`} {
		if _, err := ParseConfig(raw); err == nil {
			t.Fatalf("accepted unsafe configuration %s", raw)
		}
	}
	c, err := ParseConfig(`{"owner_account_id":"operator","demo_enabled":true,"allow_live":false}`)
	if err != nil || !c.DemoEnabled {
		t.Fatal(c, err)
	}
	p, err := Build(c)
	if err != nil || p != nil {
		t.Fatal("demo caused network provider")
	}
	if !strings.Contains(ErrUnavailable.Error(), "not configured") {
		t.Fatal("bad error")
	}
}
