package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/pkg/client"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBillingPaymentJourney(t *testing.T) {
	base, bootstrap := os.Getenv("CONTINUITY_E2E_URL"), os.Getenv("CONTINUITY_E2E_BOOTSTRAP_TOKEN")
	if base == "" || bootstrap == "" {
		t.Skip("local E2E server required")
	}
	_, token := createAccount(t, base, bootstrap, "billing-e2e-"+time.Now().Format("150405"))
	headers := map[string]string{"Content-Type": "application/json"}
	call := func(method, path string, body any, status int, key string) []byte {
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		h := map[string]string{"Content-Type": "application/json"}
		if key != "" {
			h["Idempotency-Key"] = key
		}
		return request(t, method, base+path, token, b, h, status)
	}
	var config struct {
		Providers []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"providers"`
	}
	_ = json.Unmarshal(call("GET", "/v1/billing/providers", nil, 200, ""), &config)
	enabled := false
	for _, p := range config.Providers {
		if p.ID == "demo" && p.Enabled {
			enabled = true
		}
	}
	if !enabled {
		t.Skip("simulation provider disabled")
	}
	var account protocol.BillingAccount
	_ = json.Unmarshal(call("POST", "/v1/billing/accounts", map[string]string{"plan": "pro", "currency": "CNY"}, 201, ""), &account)
	var archiveTask protocol.Task
	_ = json.Unmarshal(call("POST", "/v1/tasks", map[string]string{"title": "归档 → 账单 → 支付回执 E2E", "objective": "Verify local payment accounting without real money"}, 201, ""), &archiveTask)
	var archiveConfig struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.Unmarshal(call("GET", "/v1/storage/config", nil, 200, ""), &archiveConfig)
	if archiveConfig.Enabled {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		sdk := client.New(base, token)
		payload := []byte("# K8s 闭环测试\nSDK 接收 → JetStream → Worker → 对象存储 → 存储用量 → 账单与回执。")
		if _, err := sdk.UploadContent(ctx, archiveTask.ID, "billing-archive", "v1", "conversation.md", "text/markdown", payload, ""); err != nil {
			t.Fatal(err)
		}
		page, err := sdk.ListStorageJobs(ctx, archiveTask.ID, 0)
		if err != nil || len(page.Jobs) != 1 {
			t.Fatalf("durable outbox: %+v %v", page, err)
		}
		job, err := sdk.WaitStorageJob(ctx, archiveTask.ID, page.Jobs[0].ID, 40*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if expected := os.Getenv("CONTINUITY_E2E_ARCHIVE_TARGET"); expected != "" && job.TargetID != expected {
			t.Fatalf("archive routed to %s, expected %s", job.TargetID, expected)
		}
		body := call("GET", fmt.Sprintf("/v1/tasks/%s/storage-jobs/%d/content", archiveTask.ID, job.ID), nil, 200, "")
		if !bytes.Equal(body, payload) {
			t.Fatal("cloud readback differs")
		}
	}
	// Use the service's clock: Rancher VM and host clocks can temporarily drift.
	clock, err := http.Head(base + "/v1/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	clock.Body.Close()
	serverTime, err := http.ParseTime(clock.Header.Get("Date"))
	if err != nil {
		t.Fatal(err)
	}
	end := serverTime.Add(time.Second) // include current second; API allows <=1s tolerance
	input := map[string]any{"billing_account_id": account.ID, "period_start": end.Add(-time.Hour), "period_end": end}
	var doc protocol.BillingDocument
	_ = json.Unmarshal(call("POST", "/v1/billing/invoices", input, 201, "e2e-invoice-key"), &doc)
	if doc.Invoice.AmountMinor <= 0 {
		t.Fatal("bill has no consumption")
	}
	if archiveConfig.Enabled {
		seen := map[string]bool{}
		for _, line := range doc.Invoice.Lines {
			seen[line.Metric] = true
		}
		for _, metric := range []string{"storage.archive.bytes", "storage.retained.byte_hours", "storage.download.bytes"} {
			if !seen[metric] {
				t.Fatal("invoice missing", metric)
			}
		}
	}
	id := doc.Invoice.ID
	var again protocol.BillingDocument
	_ = json.Unmarshal(call("POST", "/v1/billing/invoices", input, 201, "e2e-invoice-key"), &again)
	if again.Invoice.ID != id {
		t.Fatal("idempotent issue created a duplicate invoice")
	}
	var checkout struct {
		Payment protocol.BillingPayment `json:"payment"`
		Link    string                  `json:"payment_link"`
	}
	_ = json.Unmarshal(call("POST", "/v1/billing/invoices/"+id+"/payments", map[string]string{"provider": "demo"}, 201, "e2e-payment-key"), &checkout)
	u, _ := url.Parse(checkout.Link)
	page := request(t, "GET", base+u.RequestURI(), "", nil, nil, 200)
	if !strings.Contains(string(page), "本地模拟") {
		t.Fatal("simulation safety warning missing")
	}
	// Exercise PostgreSQL optimistic concurrency: exactly one receipt under callback retries.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", base+"/v1/billing/invoices/"+id+"/payments/"+checkout.Payment.ID+"/simulate", strings.NewReader(`{"outcome":"succeeded"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				errs <- fmt.Errorf("simulate returned %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	_ = json.Unmarshal(call("GET", "/v1/billing/invoices/"+id, nil, 200, ""), &doc)
	if doc.Invoice.Status != "paid" || len(doc.Receipts) != 1 {
		t.Fatalf("invalid settlement %+v", doc)
	}
	call("GET", "/v1/billing/invoices/"+id+"/export", nil, 200, "")
	call("GET", "/v1/billing/invoices/"+id+"/receipts/"+doc.Receipts[0].ID+"?format=html", nil, 200, "")
	_, other := createAccount(t, base, bootstrap, "billing-other-"+time.Now().Format("150405"))
	request(t, "GET", base+"/v1/billing/invoices/"+id, other, nil, headers, 404)
	call("POST", "/v1/billing/invoices/"+id+"/payments/"+checkout.Payment.ID+"/revoke-link", nil, 200, "")
	request(t, "GET", base+u.RequestURI(), "", nil, nil, 404)
}
