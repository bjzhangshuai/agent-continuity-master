package payments

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	PublicBaseURL string        `json:"public_base_url"`
	AllowLive     bool          `json:"allow_live"`
	DemoEnabled   bool          `json:"demo_enabled"`
	Wechat        *WechatConfig `json:"wechat,omitempty"`
	Alipay        *AlipayConfig `json:"alipay,omitempty"`
	PayPal        *PayPalConfig `json:"paypal,omitempty"`
	Stripe        *StripeConfig `json:"stripe,omitempty"`
}
type WechatConfig struct {
	AppID            string            `json:"app_id"`
	MerchantID       string            `json:"merchant_id"`
	Serial           string            `json:"certificate_serial"`
	PrivateKeyFile   string            `json:"private_key_file"`
	APIv3Key         string            `json:"api_v3_key"`
	PlatformKeyFiles map[string]string `json:"platform_key_files"`
}
type AlipayConfig struct {
	AppID          string `json:"app_id"`
	SellerID       string `json:"seller_id"`
	PrivateKeyFile string `json:"private_key_file"`
	PublicKeyFile  string `json:"public_key_file"`
	Sandbox        bool   `json:"sandbox"`
}
type PayPalConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	MerchantID   string `json:"merchant_id"`
	WebhookID    string `json:"webhook_id"`
	Sandbox      bool   `json:"sandbox"`
}
type StripeConfig struct {
	SecretKey     string `json:"secret_key"`
	WebhookSecret string `json:"webhook_secret"`
	Sandbox       bool   `json:"sandbox"`
}

func ParseConfig(raw string) (Config, error) {
	var c Config
	if raw == "" {
		return c, nil
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("payment configuration JSON: %w", err)
	}
	return c, nil
}
func Build(c Config) (*Registry, error) {
	ps := []Provider{}
	configured := c.Wechat != nil || c.Alipay != nil || c.PayPal != nil || c.Stripe != nil
	if c.DemoEnabled || configured {
		u, err := url.Parse(c.PublicBaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
			return nil, errors.New("payments require a public_base_url origin")
		}
		loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
		if u.Scheme != "https" && !(u.Scheme == "http" && loopback && !configured) {
			return nil, errors.New("network payments require an HTTPS public_base_url; HTTP loopback is simulation-only")
		}
	}
	if c.DemoEnabled {
		ps = append(ps, Demo{})
	}
	if c.Wechat != nil {
		if !c.AllowLive {
			return nil, errors.New("WeChat APIv3 uses the live network; allow_live must explicitly be true")
		}
		p, err := newWechat(*c.Wechat)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	if c.Alipay != nil {
		if !c.Alipay.Sandbox && !c.AllowLive {
			return nil, errors.New("live Alipay requires allow_live")
		}
		p, err := newAlipay(*c.Alipay)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	if c.PayPal != nil {
		v := *c.PayPal
		if !v.Sandbox && !c.AllowLive {
			return nil, errors.New("live PayPal requires allow_live")
		}
		if v.ClientID == "" || v.ClientSecret == "" || v.MerchantID == "" || v.WebhookID == "" {
			return nil, errors.New("PayPal requires client_id, client_secret, merchant_id and webhook_id")
		}
		base := "https://api-m.paypal.com"
		if v.Sandbox {
			base = "https://api-m.sandbox.paypal.com"
		}
		ps = append(ps, &PayPal{config: v, base: base, http: client()})
	}
	if c.Stripe != nil {
		v := *c.Stripe
		if !v.Sandbox && !c.AllowLive {
			return nil, errors.New("live Stripe requires allow_live")
		}
		prefix := "sk_live_"
		if v.Sandbox {
			prefix = "sk_test_"
		}
		if !strings.HasPrefix(v.SecretKey, prefix) || !strings.HasPrefix(v.WebhookSecret, "whsec_") {
			return nil, errors.New("Stripe keys must match the configured environment")
		}
		ps = append(ps, &Stripe{config: v, base: "https://api.stripe.com", http: client()})
	}
	return NewRegistry(ps...), nil
}

func readKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("payment signing key file required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read signing key file: %w", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, errors.New("empty signing key file")
	}
	return b, nil
}
func environment(sandbox bool) string {
	if sandbox {
		return "sandbox"
	}
	return "live"
}
