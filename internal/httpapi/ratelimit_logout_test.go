package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogoutIndependentOfExhaustedLoginBudget(t *testing.T) {
	h := newRateLimiter().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	call := func(path string) int {
		r := httptest.NewRequest("POST", path, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for n := 0; n < 20; n++ {
		if got := call("/v1/auth/login"); got != 204 {
			t.Fatal(got)
		}
	}
	if got := call("/v1/auth/login"); got != 429 {
		t.Fatal(got)
	}
	for n := 0; n < 20; n++ {
		if got := call("/v1/auth/session/logout"); got != 204 {
			t.Fatal(got)
		}
	}
	if got := call("/v1/auth/session/logout"); got != 429 {
		t.Fatal(got)
	}
}
