//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"remota.local/remota/internal/server/store"
)

// The production entry point runs in a separate process; the test kills it and
// verifies durable credentials, identity invalidation, and operation lookup.
func TestServerProcessRestart(t *testing.T) {
	if dir := os.Getenv("REMOTA_SERVER_TEST_STATE"); dir != "" {
		err := run([]string{"serve", "--state-dir", dir, "--listen", os.Getenv("REMOTA_SERVER_TEST_LISTEN")}, io.Discard, os.Stderr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	var handoffJSON bytes.Buffer
	if err := run([]string{"init", "--state-dir", dir}, &handoffJSON, io.Discard); err != nil {
		t.Fatal(err)
	}
	var handoff store.Handoff
	if err := json.Unmarshal(handoffJSON.Bytes(), &handoff); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	base := "http://" + address
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	var child *exec.Cmd
	var done chan error
	stop := func() {
		if child != nil {
			child.Process.Kill()
			<-done
			child = nil
		}
	}
	t.Cleanup(stop)
	start := func() string {
		child = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServerProcessRestart$")
		child.Env = append(os.Environ(), "REMOTA_SERVER_TEST_STATE="+dir, "REMOTA_SERVER_TEST_LISTEN="+address)
		var stderr bytes.Buffer
		child.Stderr = &stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		done = make(chan error, 1)
		go func() { done <- child.Wait() }()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			r, err := client.Get(base + "/healthz")
			if err == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					return r.Header.Get("X-Remota-Epoch")
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		t.Fatal("server did not start")
		return ""
	}
	request := func(method, path, body string, cookie *http.Cookie, csrf string) (int, []byte, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", base)
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		return r.StatusCode, b, r.Header
	}
	login := func() (*http.Cookie, string) {
		t.Helper()
		b, err := json.Marshal(map[string]any{"password": handoff.AdminPassword})
		if err != nil {
			t.Fatal(err)
		}
		status, body, header := request("POST", "/api/v1/admin/login", string(b), nil, "")
		if status != 200 {
			t.Fatalf("login failed: %d", status)
		}
		var result struct {
			CSRF string `json:"csrf_token"`
		}
		if err = json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		cookies := (&http.Response{Header: header}).Cookies()
		if len(cookies) != 1 {
			t.Fatal("missing cookie")
		}
		return cookies[0], result.CSRF
	}
	epoch := start()
	oldCookie, csrf := login()
	op := strings.Repeat("a", 32)
	body := fmt.Sprintf(`{"operation_id":%q,"kind":"access","name":"test"}`, op)
	status, issued, _ := request("POST", "/api/v1/admin/credentials", body, oldCookie, csrf)
	if status != 201 {
		t.Fatalf("credential issue failed: %d", status)
	}
	var credential struct {
		Secret     string `json:"secret"`
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
	}
	if err = json.Unmarshal(issued, &credential); err != nil {
		t.Fatal(err)
	}
	stop()
	newEpoch := start()
	if newEpoch == "" || newEpoch == epoch {
		t.Fatal("server epoch did not change")
	}
	status, _, _ = request("GET", "/api/v1/admin/state", "", oldCookie, "")
	if status != 401 {
		t.Fatal("old administrator session survived restart")
	}
	newCookie, newCSRF := login()
	status, state, _ := request("GET", "/api/v1/admin/state", "", newCookie, "")
	if status != 200 || !bytes.Contains(state, []byte(op)) || bytes.Contains(state, []byte(credential.Secret)) {
		t.Fatal("durable operation lookup or secret visibility differs")
	}
	status, bodyBytes, _ := request("POST", "/api/v1/admin/credentials", body, newCookie, newCSRF)
	if status != 409 || !bytes.Contains(bodyBytes, []byte(credential.Credential.ID)) || bytes.Contains(bodyBytes, []byte(credential.Secret)) {
		t.Fatal("repeated issuance was replayed")
	}
}
