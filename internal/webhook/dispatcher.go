package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/secrets"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

const (
	MaxAttempts = 12
	maxResponse = 1 << 20
)

type Dispatcher struct {
	store  storage.Store
	cipher *secrets.Cipher
	client *http.Client
	logger *slog.Logger
	now    func() time.Time
}

func New(store storage.Store, cipher *secrets.Cipher, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{store: store, cipher: cipher, client: safeHTTPClient(), logger: logger, now: time.Now}
}

func (d *Dispatcher) SetClientForTest(client *http.Client) { d.client = client }

func ValidateEndpointURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("webhook URL must be an absolute HTTPS URL without user information")
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && unsafeIP(ip) {
		return errors.New("webhook URL cannot target a private or local address")
	}
	return nil
}

func (d *Dispatcher) Run(ctx context.Context) {
	d.dispatchReady(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.dispatchReady(ctx)
		}
	}
}

func (d *Dispatcher) dispatchReady(ctx context.Context) {
	items, err := d.store.ListReadyWebhookDeliveries(ctx, d.now().UTC(), 50)
	if err != nil {
		d.logger.Error("list webhook deliveries", "error", err)
		return
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		if err := d.Deliver(ctx, item); err != nil {
			d.logger.Warn("webhook delivery failed", "delivery_id", item.ID, "attempt", item.Attempts+1, "error", err)
		}
	}
}

func (d *Dispatcher) Deliver(ctx context.Context, delivery protocol.WebhookDelivery) error {
	now := d.now().UTC()
	delivery.Attempts++
	endpoint, encryptedSecret, err := d.store.GetWebhookEndpoint(ctx, delivery.WebhookID)
	if err != nil {
		return d.fail(ctx, &delivery, now, 0, "webhook endpoint unavailable: "+err.Error())
	}
	secret, err := d.cipher.Decrypt(encryptedSecret, endpoint.AccountID+":"+endpoint.ID)
	if err != nil && len(encryptedSecret) > 0 && encryptedSecret[0] == 1 {
		secret, err = d.cipher.Decrypt(encryptedSecret, endpoint.ID)
	}
	if err != nil {
		return d.fail(ctx, &delivery, now, 0, err.Error())
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.URL, bytes.NewReader(delivery.Payload))
	if err != nil {
		return d.fail(ctx, &delivery, now, 0, err.Error())
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(delivery.Payload)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agent-continuity-webhook/1")
	req.Header.Set("X-Agent-Continuity-Delivery", delivery.ID)
	req.Header.Set("X-Agent-Continuity-Event", delivery.EventType)
	req.Header.Set("X-Agent-Continuity-Signature", "v1="+hex.EncodeToString(mac.Sum(nil)))
	response, err := d.client.Do(req)
	if err != nil {
		return d.fail(ctx, &delivery, now, 0, err.Error())
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponse))
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return d.fail(ctx, &delivery, now, response.StatusCode, "unexpected HTTP status "+strconv.Itoa(response.StatusCode))
	}
	delivery.LastStatus = response.StatusCode
	delivery.LastError = ""
	delivery.DeliveredAt = &now
	return d.store.UpdateWebhookDelivery(ctx, delivery)
}

func (d *Dispatcher) fail(ctx context.Context, delivery *protocol.WebhookDelivery, now time.Time, status int, message string) error {
	delivery.LastStatus = status
	delivery.LastError = truncate(message, 1024)
	if delivery.Attempts >= MaxAttempts {
		delivery.DeadLetteredAt = &now
	} else {
		backoff := time.Second << min(delivery.Attempts, 11)
		if backoff > time.Hour {
			backoff = time.Hour
		}
		delivery.NextAttempt = now.Add(backoff)
	}
	if err := d.store.UpdateWebhookDelivery(ctx, *delivery); err != nil {
		return err
	}
	return errors.New(delivery.LastError)
}

func safeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		// Webhooks intentionally bypass process proxy variables: otherwise a
		// public URL could be routed through a local proxy and evade the direct
		// destination address checks below.
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("resolve webhook host: %w", err)
		}
		for _, resolved := range addresses {
			if unsafeIP(resolved.IP) {
				return nil, errors.New("webhook resolved to a private or local address")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
	}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return errors.New("webhook redirects are disabled")
	}}
}

func unsafeIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast()
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
