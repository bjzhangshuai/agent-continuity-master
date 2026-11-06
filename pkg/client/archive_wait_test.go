package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWaitStorageReceiptTerminalStates(t *testing.T) {
	for _, state := range []string{"stored", "dead", "deleted"} {
		t.Run(state, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"id":1,"state":"` + state + `"}`))
			}))
			defer s.Close()
			_, err := New(s.URL, "test").WaitStorageJob(context.Background(), "task", 1, time.Second)
			if (err == nil) != (state == "stored") {
				t.Fatal(state, err)
			}
		})
	}
}
