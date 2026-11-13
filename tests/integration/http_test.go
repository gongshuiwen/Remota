package integration

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	json "encoding/json/v2"
	"remota.local/remota/internal/contracttest"
	"remota.local/remota/internal/protocol"
)

type object = map[string]any

// The helper protocol stays on the child process's private stdin/stdout.
type serverCommand struct{ Fault, TaskID string }
type requestCounts struct{ Starts, Results, LostReplies int }
type serverReply struct {
	URL, Access, MCP, Password string `json:",omitempty"`
	requestCounts
	PollFailures int `json:",omitempty"`
}

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	eventuallyEvery(t, timeout, 40*time.Millisecond, condition)
}

func eventuallyEvery(t *testing.T, timeout, interval time.Duration, condition func() bool) {
	t.Helper()
	end := time.NewTimer(timeout)
	defer end.Stop()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for !condition() {
		select {
		case <-end.C:
			t.Fatal("timed out waiting for end-to-end observation")
		case <-tick.C:
		}
	}
}

type httpPeer struct {
	client    *http.Client
	validator *protocol.Validator
	url       string
}

func (p *httpPeer) exchange(t *testing.T, method, path string, body any, headers http.Header, status int) (object, http.Header) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require(t, err)
		reader = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, p.url+path, reader)
	require(t, err)
	r.Header = headers.Clone()
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(r)
	require(t, err)
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("%s %s: HTTP %d, want %d", method, path, response.StatusCode, status)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	require(t, err)
	if path != "/mcp" {
		contract, err := contracttest.Load()
		require(t, err)
		require(t, contract.Check(r, response.StatusCode, response.Header, b))
		if len(b) == 0 {
			return nil, response.Header
		}
	} else {
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("MCP response is not marked no-store")
		}
		if status == 202 || status == 204 {
			if len(b) != 0 {
				t.Fatal("unexpected MCP response body")
			}
			return nil, response.Header
		}
		media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			t.Fatal("MCP response is not JSON")
		}
	}
	v, err := protocol.Parse(b)
	require(t, err)
	return v.(object), response.Header
}

type mcpClient struct {
	peer           *httpPeer
	token, session string
	next           int
}

func newMCPClient(t *testing.T, peer *httpPeer, token string) *mcpClient {
	t.Helper()
	m := &mcpClient{peer: peer, token: token}
	v, h := m.rpc(t, "initialize", object{"protocolVersion": "2025-11-25", "capabilities": object{},
		"clientInfo": object{"name": "Windows acceptance", "version": "1"}}, "MCPInitializeResponse")
	m.session = h.Get("Mcp-Session-Id")
	if m.session == "" || v["result"].(object)["protocolVersion"] != "2025-11-25" {
		t.Fatal("MCP initialization did not negotiate the contract")
	}
	peer.exchange(t, "POST", "/mcp", object{"jsonrpc": "2.0", "method": "notifications/initialized"}, m.headers(), 202)
	t.Cleanup(func() {
		// A revoked or explicitly ended client is already closed.
		r, err := http.NewRequest("DELETE", peer.url+"/mcp", nil)
		if err == nil {
			r.Header = m.headers()
			if response, err := peer.client.Do(r); err == nil {
				response.Body.Close()
			}
		}
	})
	return m
}

func (m *mcpClient) headers() http.Header {
	h := http.Header{"Authorization": {"Bearer " + m.token}, "Accept": {"application/json, text/event-stream"}}
	if m.session != "" {
		h.Set("Mcp-Session-Id", m.session)
		h.Set("MCP-Protocol-Version", "2025-11-25")
	}
	return h
}

func (m *mcpClient) rpc(t *testing.T, method string, params object, model string) (object, http.Header) {
	t.Helper()
	m.next++
	id := fmt.Sprint(m.next)
	v, h := m.peer.exchange(t, "POST", "/mcp", object{"jsonrpc": "2.0", "id": id, "method": method, "params": params}, m.headers(), 200)
	require(t, m.peer.validator.Validate(model, v))
	if v["id"] != id {
		t.Fatal("MCP response ID changed")
	}
	return v, h
}

func (m *mcpClient) ok(t *testing.T, name string, args object) object {
	t.Helper()
	v, _ := m.rpc(t, "tools/call", object{"name": name, "arguments": args}, "MCPToolResponse")
	r := v["result"].(object)
	body := r["structuredContent"].(object)
	text, err := protocol.Parse([]byte(r["content"].([]any)[0].(object)["text"].(string)))
	require(t, err)
	if !reflect.DeepEqual(text, body) || r["isError"] != (body["ok"] != true) {
		t.Fatal("MCP tool representations disagree")
	}
	models := map[string]string{"list_sessions": "SessionsToolBody", "get_session": "SessionToolBody", "ping_agent": "PingToolBody",
		"get_agent_info": "InfoToolBody", "request_command": "CommandToolBody", "get_task": "TaskToolBody", "cancel_task": "CancelToolBody"}
	require(t, m.peer.validator.Validate(models[name], body))
	if body["ok"] != true {
		t.Fatalf("%s: %s", name, body["error"].(object)["code"])
	}
	return body["data"].(object)
}

func (m *mcpClient) task(t *testing.T, sid, tid string) object {
	t.Helper()
	return m.ok(t, "get_task", object{"session_id": sid, "task_id": tid})["task"].(object)
}

func quotePowerShell(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
