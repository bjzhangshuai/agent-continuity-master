// tenant-demo creates isolated, idempotent local examples without resetting old data.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/tenancy"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type demoUser struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
}
type window struct {
	From time.Time `json:"period_start"`
	To   time.Time `json:"period_end"`
}
type demoState struct {
	Users    map[string]demoUser `json:"users"`
	Tenants  map[string]string   `json:"tenants"`
	Invoices map[string]string   `json:"invoices"`
	Windows  map[string]window   `json:"windows"`
}
type apiClient struct {
	base, token string
	http        *http.Client
}

func (a *apiClient) call(method, path string, input, out any, key string, expected ...int) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	r, err := http.NewRequest(method, a.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Portal-Request", "1")
	if a.token != "" {
		r.Header.Set("Authorization", "Bearer "+a.token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	resp, err := a.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	valid := len(expected) == 0 && resp.StatusCode >= 200 && resp.StatusCode < 300
	for _, code := range expected {
		valid = valid || resp.StatusCode == code
	}
	if !valid {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, data)
	}
	if out != nil && resp.StatusCode != 409 {
		return json.Unmarshal(data, out)
	}
	return nil
}
func newClient(base, token string) *apiClient {
	jar, _ := cookiejar.New(nil)
	return &apiClient{base: base, token: token, http: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
}
func save(path string, state demoState) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tenant-demo-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func main() {
	base := flag.String("url", "http://127.0.0.1:18080", "Loopback demo service")
	credentials := flag.String("credentials", "", "Existing local super-admin credential file")
	stateFile := flag.String("state", ".data/tenant-demo.json", "Private resumable demo state (0600)")
	flag.Parse()
	u, e := url.Parse(*base)
	if e != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		must(errors.New("refusing non-loopback seed"))
	}
	raw, e := os.ReadFile(*credentials)
	must(e)
	var credential struct {
		Token     string `json:"access_token"`
		AccountID string `json:"account_id"`
	}
	must(json.Unmarshal(raw, &credential))
	root := newClient(*base, credential.Token)
	var identity struct {
		Super   bool                        `json:"super_admin"`
		Managed []struct{ ID, Name string } `json:"managed_tenants"`
		Grants  []struct {
			Grant tenancy.Grant `json:"grant"`
		} `json:"grants"`
	}
	must(root.call("GET", "/v1/portal/session", nil, &identity, ""))
	if !identity.Super {
		must(errors.New("explicitly configured platform super-admin credential required"))
	}
	var providers struct {
		Book struct {
			Test bool `json:"test_mode"`
		} `json:"price_book"`
		Providers []struct {
			ID          string `json:"id"`
			Enabled     bool   `json:"enabled"`
			Environment string `json:"environment"`
		} `json:"providers"`
	}
	must(root.call("GET", "/v1/billing/providers", nil, &providers, ""))
	demo := false
	for _, p := range providers.Providers {
		if p.ID == "demo" && p.Enabled && p.Environment == "simulation" {
			demo = true
		}
	}
	if !providers.Book.Test || !demo {
		must(errors.New("simulation price book and demo channel must be enabled"))
	}
	state := demoState{Users: map[string]demoUser{}, Tenants: map[string]string{}, Invoices: map[string]string{}, Windows: map[string]window{}}
	if raw, e = os.ReadFile(*stateFile); e == nil {
		must(json.Unmarshal(raw, &state))
	} else if !os.IsNotExist(e) {
		must(e)
	}
	must(os.MkdirAll(filepath.Dir(*stateFile), 0700))
	users := []struct{ key, name string }{{"platform", "平台超级管理员"}, {"group", "星海集团所有者"}, {"east", "星海华东所有者"}, {"lab", "杭州研发中心所有者"}, {"west", "星海华南所有者"}, {"independent", "北辰智能所有者"}, {"finance", "集团财务付款人"}, {"viewer", "华东只读审计成员"}, {"hq", "平台总部管理员"}, {"region", "平台华东区域管理员"}, {"support", "平台客服"}}
	for _, spec := range users {
		if _, exists := state.Users[spec.key]; !exists {
			b := make([]byte, 24)
			_, e = rand.Read(b)
			must(e)
			state.Users[spec.key] = demoUser{Username: "tenant-demo." + spec.key, Password: base64.RawURLEncoding.EncodeToString(b), Name: spec.name}
		}
	}
	must(save(*stateFile, state))
	clients := map[string]*apiClient{}
	for _, spec := range users {
		user := state.Users[spec.key]
		input := map[string]any{"username": user.Username, "password": user.Password, "display_name": user.Name, "self": spec.key == "platform"}
		must(root.call("POST", "/v1/portal/users", input, nil, "", 201, 409))
		a := newClient(*base, "")
		var signed struct {
			AccountID string `json:"account_id"`
		}
		must(a.call("POST", "/v1/auth/login", map[string]string{"username": user.Username, "password": user.Password}, &signed, ""))
		user.AccountID = signed.AccountID
		state.Users[spec.key] = user
		clients[spec.key] = a
		must(save(*stateFile, state))
	}
	specs := []struct{ key, name, parent, owner string }{{"group", "星海集团 · 多租户演示", "", "group"}, {"east", "星海华东公司 · 集团代付", "group", "east"}, {"lab", "杭州研发中心 · 三级租户", "east", "lab"}, {"west", "星海华南公司 · 独立结算", "group", "west"}, {"independent", "北辰智能 · 独立客户", "", "independent"}}
	for _, spec := range specs {
		if state.Tenants[spec.key] != "" {
			continue
		}
		for _, o := range identity.Managed {
			if o.Name == spec.name {
				state.Tenants[spec.key] = o.ID
			}
		}
		if state.Tenants[spec.key] == "" {
			var o protocol.Organization
			must(root.call("POST", "/v1/portal/tenants", map[string]string{"name": spec.name, "slug": "tenant-demo-" + spec.key, "parent_id": state.Tenants[spec.parent], "owner_account_id": state.Users[spec.owner].AccountID, "plan": "free"}, &o, ""))
			state.Tenants[spec.key] = o.ID
		}
		must(save(*stateFile, state))
	}
	for _, m := range []struct{ tenant, owner, user, role string }{{"group", "group", "finance", "billing"}, {"east", "east", "viewer", "viewer"}, {"west", "west", "finance", "viewer"}} {
		must(clients[m.owner].call("PUT", "/v1/organizations/"+state.Tenants[m.tenant]+"/members/"+state.Users[m.user].AccountID, map[string]string{"role": m.role}, nil, ""))
	}
	grant := func(a *apiClient, user, role, tenant, parent string) string {
		for _, entry := range identity.Grants {
			g := entry.Grant
			if g.AccountID == state.Users[user].AccountID && g.Role == role && g.TenantID == tenant {
				return g.ID
			}
		}
		var g tenancy.Grant
		must(a.call("POST", "/v1/portal/grants", map[string]string{"account_id": state.Users[user].AccountID, "role": role, "tenant_id": tenant, "parent_id": parent}, &g, ""))
		return g.ID
	}
	hq := grant(root, "hq", "admin", "*", "")
	region := grant(clients["hq"], "region", "admin", state.Tenants["east"], hq)
	grant(clients["region"], "support", "support", state.Tenants["lab"], region)
	for _, spec := range specs {
		must(clients[spec.owner].call("POST", "/v1/portal/tenants/"+state.Tenants[spec.key]+"/billing-account", map[string]string{"currency": "CNY"}, nil, ""))
	}
	var groupView struct {
		Mandates []tenancy.Mandate `json:"mandates"`
	}
	must(clients["group"].call("GET", "/v1/portal/session", nil, &groupView, ""))
	var mandate tenancy.Mandate
	for _, m := range groupView.Mandates {
		if m.TenantID == state.Tenants["east"] {
			mandate = m
		}
	}
	if mandate.ID == "" {
		must(clients["east"].call("POST", "/v1/portal/mandates", map[string]string{"tenant_id": state.Tenants["east"], "payer_tenant_id": state.Tenants["group"]}, &mandate, ""))
	}
	if mandate.Status == "pending" {
		must(clients["group"].call("POST", "/v1/portal/mandates/"+mandate.ID+"/accept", nil, nil, ""))
	}
	for _, scenario := range []struct {
		key, tenant, owner string
		tasks              int
	}{{"east-paid", "east", "east", 6}, {"east-open", "east", "east", 4}, {"lab-open", "lab", "lab", 3}, {"west-open", "west", "west", 5}, {"independent-open", "independent", "independent", 4}, {"group-open", "group", "group", 4}} {
		id := state.Tenants[scenario.tenant]
		a := clients[scenario.owner]
		if state.Invoices[scenario.key] != "" {
			continue
		}
		var old struct {
			Invoices []protocol.BillingDocument `json:"invoices"`
		}
		must(a.call("GET", "/v1/portal/tenants/"+id+"/billing", nil, &old, ""))
		key := "tenant-demo-v1-" + scenario.key
		for _, d := range old.Invoices {
			if d.Invoice.IdempotencyKey == key {
				state.Invoices[scenario.key] = d.Invoice.ID
			}
		}
		if state.Invoices[scenario.key] != "" {
			must(save(*stateFile, state))
			continue
		}
		w := state.Windows[scenario.key]
		if w.To.IsZero() {
			w.From = time.Now().UTC().Add(-24 * time.Hour)
			for _, d := range old.Invoices {
				if d.Invoice.OrganizationID == id && d.Invoice.PeriodEnd.After(w.From) {
					w.From = d.Invoice.PeriodEnd
				}
			}
			for n := 0; n < scenario.tasks; n++ {
				var task protocol.Task
				must(a.call("POST", "/v1/tasks", map[string]any{"title": fmt.Sprintf("多租户演示 · %s · Agent 协作 %02d", scenario.key, n+1), "objective": "演示租户隔离、成员用量汇总和授权付款，不执行外部工具。", "organization_id": id, "metadata": map[string]any{"demo": true, "scenario": scenario.key, "health_status": []string{"healthy", "attention", "completed"}[n%3], "progress": 40 + n*10}}, &task, ""))
			}
			var usage struct {
				Usage []protocol.UsageRecord `json:"usage"`
			}
			must(a.call("GET", "/v1/usage?limit=500", nil, &usage, ""))
			for _, v := range usage.Usage {
				if v.OrganizationID == id && v.RecordedAt.After(w.To) {
					w.To = v.RecordedAt
				}
			}
			if w.To.IsZero() {
				must(errors.New("tenant usage missing"))
			}
			w.To = w.To.Add(time.Microsecond)
			state.Windows[scenario.key] = w
			must(save(*stateFile, state))
		}
		var invoice protocol.BillingDocument
		must(a.call("POST", "/v1/portal/tenants/"+id+"/invoices", w, &invoice, key))
		state.Invoices[scenario.key] = invoice.Invoice.ID
		must(save(*stateFile, state))
		fmt.Printf("Created %s: %s, %d %s minor units\n", scenario.key, invoice.Invoice.ID, invoice.Invoice.AmountMinor, invoice.Invoice.Currency)
	}
	id := state.Invoices["east-paid"]
	var bill protocol.BillingDocument
	must(clients["finance"].call("GET", "/v1/billing/invoices/"+id, nil, &bill, ""))
	if bill.Invoice.Status == "open" {
		var p struct {
			Payment protocol.BillingPayment `json:"payment"`
		}
		must(clients["finance"].call("POST", "/v1/billing/invoices/"+id+"/payments", map[string]string{"provider": "demo"}, &p, "tenant-demo-paid-0001"))
		must(clients["finance"].call("POST", "/v1/billing/invoices/"+id+"/payments/"+p.Payment.ID+"/simulate", map[string]string{"outcome": "succeeded"}, nil, ""))
	}
	must(root.call("POST", "/v1/treasury/collections/import", map[string]string{"invoice_id": id}, nil, ""))
	fmt.Printf("Demo ready: %d login accounts, %d tenants, %d invoices. Private credentials: %s (0600). Existing data preserved.\n", len(state.Users), len(state.Tenants), len(state.Invoices), *stateFile)
}
