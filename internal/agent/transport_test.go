package agent

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransportAddressAndRedirectBoundaries(t *testing.T) {
	for _, address := range []string{"http://localhost:8080", "https://example.com/path", "https://user@example.com", "https://example.com#", "http://192.0.2.1"} {
		if _, err := NewTransport(address, false); err == nil {
			t.Fatalf("unsafe root accepted: %q", address)
		}
	}
	if _, err := NewTransport("http://192.0.2.1", true); err != nil {
		t.Fatal(err)
	}
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	tx, err := NewTransport(redirect.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = tx.request(context.Background(), "POST", "/sessions", "sensitive", "", map[string]any{}, 201, "Registration")
	if !errors.Is(err, ErrProtocol) || forwarded.Load() != 0 {
		t.Fatal("credential-bearing redirect was followed")
	}
}

func TestTransportResponses(t *testing.T) {
	response := fixture(t, "poll.readonly")
	response["session"].(map[string]any)["granted_capabilities"] = []string{"ping", "info", "exec"}
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := int(status.Load())
		if code == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(code)
			return
		}
		w.Header().Set("X-Remota-Epoch", strings.Repeat("b", 32))
		w.Header().Set("X-Request-ID", "request-identifier")
		w.Header().Set("Content-Type", "application/json")
		if code == http.StatusOK {
			json.MarshalWrite(w, response)
			return
		}
		w.WriteHeader(code)
		w.Write([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"x","trace_id":"x"}}`))
	}))
	defer web.Close()
	tx, err := NewTransport(web.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = tx.request(context.Background(), "GET", "/sessions/unused", "token", strings.Repeat("a", 32), nil, 200, "SessionResponse")
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Status != 503 || remote.RetryAfter != 3*time.Second || !transient(err) {
		t.Fatalf("proxy failure not treated as transient: %v", err)
	}
	status.Store(http.StatusUnauthorized)
	_, _, err = tx.request(context.Background(), "GET", "/sessions/unused", "token", strings.Repeat("a", 32), nil, 200, "SessionResponse")
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("changed epoch was accepted: %v", err)
	}
	_, _, err = tx.request(context.Background(), "GET", "/sessions/unused", "token", strings.Repeat("b", 32), nil, 200, "SessionResponse")
	if !errors.As(err, &remote) || remote.Status != 401 || remote.Code != "UNAUTHENTICATED" {
		t.Fatalf("valid request identifier was rejected: %v", err)
	}
	status.Store(http.StatusOK)
	_, _, err = tx.request(context.Background(), "GET", "/sessions/unused", "token", strings.Repeat("b", 32), nil, 200, "PollResult")
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("invalid readonly capabilities accepted: %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", time.Now().Add(30*time.Second).UTC().Format(http.TimeFormat))
	if delay := retryAfter(h); delay < 28*time.Second || delay > 30*time.Second {
		t.Fatalf("HTTP-date retry delay = %v", delay)
	}
	h.Set("Retry-After", "9223372036854775807")
	if delay := retryAfter(h); delay < 24*time.Hour {
		t.Fatalf("large retry delay = %v", delay)
	}
}
