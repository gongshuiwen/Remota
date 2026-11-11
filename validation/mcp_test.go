package validation

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The product tests cover the Remota profile. This independent probe checks
// the SDK's cancellation context directly so upgrades cannot hide its behavior.
func TestMCPCancellationContextBoundary(t *testing.T) {
	ready, started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	server := mcp.NewServer(&mcp.Implementation{Name: "probe", Version: "1"}, &mcp.ServerOptions{
		SupportedProtocolVersions: []string{"2025-11-25"},
		InitializedHandler:        func(context.Context, *mcp.InitializedRequest) { close(ready) },
	})
	server.AddTool(&mcp.Tool{Name: "wait", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			close(started)
			select {
			case <-ctx.Done():
				close(stopped)
				return nil, ctx.Err()
			case <-release:
				return nil, context.Canceled
			}
		})
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	web := httptest.NewServer(transport)
	t.Cleanup(func() {
		for session := range server.Sessions() {
			_ = session.Close()
		}
		web.Close()
	})
	client := &http.Client{Timeout: 5 * time.Second}
	post := func(sid, body string) (*http.Response, error) {
		req, err := http.NewRequest("POST", web.URL, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if sid != "" {
			req.Header.Set("Mcp-Session-Id", sid)
			req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		}
		return client.Do(req)
	}
	r, err := post("", `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	sid := r.Header.Get("Mcp-Session-Id")
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || sid == "" {
		t.Fatalf("initialize: %d", r.StatusCode)
	}
	r, err = post(sid, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{"_meta":{}}}`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != 202 {
		t.Fatalf("initialized: %d", r.StatusCode)
	}
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("SDK initialization not observed")
	}
	done := make(chan error, 1)
	go func() {
		r, err := post(sid, `{"jsonrpc":"2.0","id":"waiting","method":"tools/call","params":{"name":"wait","arguments":{}}}`)
		if r != nil {
			_, _ = io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("SDK handler not called")
	}
	r, err = post(sid, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"waiting"}}`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != 202 {
		t.Fatalf("cancel: %d", r.StatusCode)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("SDK handler context not cancelled")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SDK RPC remained stuck")
	}
}
