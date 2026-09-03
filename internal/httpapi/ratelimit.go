package httpapi

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rateWindow struct {
	started time.Time
	count   int
}

type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]rateWindow
	now     func() time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{windows: map[string]rateWindow{}, now: time.Now}
}

func (l *rateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := requestLimit(r.URL.Path)
		source, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			source = r.RemoteAddr
		}
		key := source + "\x00" + rateClass(r.URL.Path)
		allowed, retry := l.allow(key, limit)
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Seconds()))))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *rateLimiter) allow(key string, limit int) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	window := l.windows[key]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	window.count++
	l.windows[key] = window
	if len(l.windows) > 10_000 {
		for candidate, value := range l.windows {
			if now.Sub(value.started) >= 2*time.Minute {
				delete(l.windows, candidate)
			}
		}
	}
	if window.count > limit {
		return false, time.Minute - now.Sub(window.started)
	}
	return true, 0
}

func requestLimit(path string) int {
	if strings.HasPrefix(path, "/v1/auth/") || path == "/v1/accounts" {
		return 20
	}
	if strings.Contains(path, "/invitations") {
		return 60
	}
	return 600
}

func rateClass(path string) string {
	if strings.HasPrefix(path, "/v1/auth/") || path == "/v1/accounts" {
		return "auth"
	}
	if strings.Contains(path, "/invitations") {
		return "invite"
	}
	return "general"
}
