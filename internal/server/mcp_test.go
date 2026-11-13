package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	json "encoding/json/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"remota.local/remota/internal/protocol"
	"remota.local/remota/internal/server/core"
)

func mcpRequest(t *testing.T, h *webHarness, token, sid, method, body string, edit func(*http.Request)) response {
	t.Helper()
	req, err := http.NewRequest(method, h.web.URL+"/mcp", strings.NewReader(body))
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
		req.Header.Set("MCP-Protocol-Version", mcpVersion)
	}
	if edit != nil {
		edit(req)
	}
	r, err := h.web.Client().Do(req)
	must(t, err)
	defer r.Body.Close()
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("MCP response Cache-Control = %q, want no-store", r.Header.Get("Cache-Control"))
	}
	b, err := io.ReadAll(r.Body)
	must(t, err)
	if r.StatusCode == 202 && len(b) != 0 {
		t.Fatal("accepted MCP notification returned a body")
	}
	value := object{}
	if len(b) != 0 {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			t.Fatalf("MCP response Content-Type = %q", r.Header.Get("Content-Type"))
		}
		must(t, json.Unmarshal(b, &value))
	}
	return response{r.StatusCode, r.Header, b, value}
}

func mcpPost(t *testing.T, h *webHarness, sid, body string) response {
	t.Helper()
	return mcpRequest(t, h, h.handoff.MCP, sid, "POST", body, nil)
}

func checkMCP(t *testing.T, h *webHarness, r response, status int, model string) {
	t.Helper()
	checkStatus(t, r, status)
	v, err := protocol.Parse(r.body)
	must(t, err)
	must(t, h.s.validator.Validate(model, v))
}

func mcpInit(t *testing.T, h *webHarness) string {
	t.Helper()
	r := mcpPost(t, h, "", `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"unsupported","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`)
	checkMCP(t, h, r, 200, "MCPInitializeResponse")
	sid := r.header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("missing MCP transport session")
	}
	return sid
}

func mcpReady(t *testing.T, h *webHarness, sid string) {
	t.Helper()
	r := mcpPost(t, h, sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	checkStatus(t, r, 202)
}

func rpcCode(t *testing.T, r response, status int, code float64) {
	t.Helper()
	if r.status != status || r.value["error"] == nil || r.value["error"].(map[string]any)["code"] != code {
		t.Fatalf("want RPC %d/%v got %d %s", status, code, r.status, r.body)
	}
}

func toolBody(t *testing.T, h *webHarness, r response, model string) object {
	t.Helper()
	checkMCP(t, h, r, 200, "MCPToolResponse")
	result := r.value["result"].(map[string]any)
	body := result["structuredContent"].(map[string]any)
	var text object
	must(t, json.Unmarshal([]byte(result["content"].([]any)[0].(map[string]any)["text"].(string)), &text))
	if !reflect.DeepEqual(body, text) || result["isError"] != (body["ok"] != true) {
		t.Fatal("tool representations disagree")
	}
	parsed, err := protocol.Parse([]byte(result["content"].([]any)[0].(map[string]any)["text"].(string)))
	must(t, err)
	must(t, h.s.validator.Validate(model, parsed))
	return body
}

func callTool(t *testing.T, h *webHarness, sid, id, name string, args object) response {
	t.Helper()
	params := object{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	b, err := json.Marshal(object{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": params})
	must(t, err)
	return mcpPost(t, h, sid, string(b))
}

func TestMCPHandshakeDiscoveryAndErrorLayers(t *testing.T) {
	h := newWeb(t, nil)
	sid := mcpInit(t, h)
	r := mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":"initial-ping","method":"ping"}`)
	if r.status != 200 || r.value["result"] == nil {
		t.Fatalf("initializing ping failed: %d %s", r.status, r.body)
	}
	r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":"early","method":"tools/list"}`)
	rpcCode(t, r, 200, -32000)
	if r.value["error"].(map[string]any)["data"].(map[string]any)["code"] != "MCP_NOT_INITIALIZED" {
		t.Fatal("initializing gate missing")
	}
	r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{"_meta":{}}}`)
	checkStatus(t, r, 202)
	mcpReady(t, h, sid)
	r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":"ping-meta","method":"ping","params":{"_meta":{"progressToken":"p"}}}`)
	if r.status != 200 || r.value["result"] == nil {
		t.Fatalf("standard ping metadata rejected: %d %s", r.status, r.body)
	}
	r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":"list","method":"tools/list"}`)
	checkMCP(t, h, r, 200, "MCPToolsListResponse")
	b, err := os.ReadFile("../../docs/contracts/acceptance/tools-list.json")
	must(t, err)
	var expected object
	must(t, json.Unmarshal(b, &expected))
	byName := func(value object) map[string]any {
		items := map[string]any{}
		for _, value := range value["result"].(map[string]any)["tools"].([]any) {
			tool := value.(map[string]any)
			items[tool["name"].(string)] = tool
		}
		return items
	}
	if !reflect.DeepEqual(byName(expected), byName(r.value)) {
		t.Fatal("SDK tool definitions differ from canonical discovery fixture")
	}
	body := toolBody(t, h, callTool(t, h, sid, "empty", "list_sessions", nil), "SessionsToolBody")
	if body["ok"] != true || len(body["data"].(map[string]any)["sessions"].([]any)) != 0 {
		t.Fatal("unexpected session list")
	}
	for _, tc := range []struct {
		name, tool string
		args       object
		code       string
	}{
		{"extra", "list_sessions", object{"extra": true}, "UNKNOWN_FIELD"},
		{"missing", "get_session", object{}, "INVALID_ARGUMENT"},
	} {
		body = toolBody(t, h, callTool(t, h, sid, tc.name, tc.tool, tc.args), map[string]string{"list_sessions": "SessionsToolBody", "get_session": "SessionToolBody"}[tc.tool])
		if body["error"].(map[string]any)["code"] != tc.code {
			t.Fatalf("%s: %v", tc.name, body)
		}
	}
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":"bad1","method":"tools/call","params":{"name":"unknown"}}`,
		`{"jsonrpc":"2.0","id":"bad2","method":"tools/call","params":{"name":"list_sessions","task":{}}}`,
		`{"jsonrpc":"2.0","id":"bad3","method":"tools/call","params":{"name":"list_sessions","arguments":null}}`,
		`{"jsonrpc":"2.0","id":"bad4","method":"tools/list","params":{"cursor":"other"}}`,
	} {
		rpcCode(t, mcpPost(t, h, sid, raw), 200, -32602)
	}
	rpcCode(t, mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":"unknown","method":"resources/list"}`), 200, -32601)
	rpcCode(t, mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":1.5,"method":"ping"}`), 400, -32600)
	rpcCode(t, mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":null,"method":"ping"}`), 400, -32600)
	rpcCode(t, mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":9007199254740992,"method":"ping"}`), 400, -32600)
	rpcCode(t, mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":1,"id":2,"method":"ping"}`), 400, -32700)
	rpcCode(t, mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":1,"method":"notifications/initialized"}`), 400, -32600)
	r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":null}}`)
	checkStatus(t, r, 202)
	r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":9007199254740991,"method":"ping"}`)
	if r.status != 200 || !strings.Contains(string(r.body), `"id":9007199254740991`) {
		t.Fatalf("RPC id not preserved: %s", r.body)
	}
	for _, id := range []string{"1.0", "1e0"} {
		r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`)
		if r.status != 200 || !strings.Contains(string(r.body), `"id":`+id) {
			t.Fatalf("integral RPC id not preserved: %s", r.body)
		}
	}
}

func TestMCPTransportIdentityAndTermination(t *testing.T) {
	h := newWeb(t, nil)
	sid := mcpInit(t, h)
	mcpReady(t, h, sid)
	ping := `{"jsonrpc":"2.0","id":"p","method":"ping"}`
	r := mcpRequest(t, h, h.handoff.MCP, sid, "GET", "", nil)
	if r.status != 405 || r.header.Get("Allow") != "POST, DELETE" {
		t.Fatal("GET surface differs")
	}
	for _, tc := range []struct {
		name   string
		edit   func(*http.Request)
		status int
		code   string
	}{
		{"origin", func(r *http.Request) { r.Header.Set("Origin", "null") }, 403, "ORIGIN_DENIED"},
		{"cookie", func(r *http.Request) { r.Header.Set("Cookie", "other=value") }, 401, "UNAUTHENTICATED"},
		{"credentials", func(r *http.Request) { r.Header.Add("Authorization", "Bearer other") }, 401, "UNAUTHENTICATED"},
		{"missing version", func(r *http.Request) { r.Header.Del("MCP-Protocol-Version") }, 400, "PROTOCOL_VERSION_REQUIRED"},
		{"wrong version", func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "wrong") }, 400, "PROTOCOL_VERSION_UNSUPPORTED"},
		{"accept", func(r *http.Request) { r.Header.Set("Accept", "application/json") }, 400, "INVALID_ARGUMENT"},
		{"unacceptable stream", func(r *http.Request) { r.Header.Set("Accept", "application/json, text/event-stream;q=0.00") }, 400, "INVALID_ARGUMENT"},
	} {
		r = mcpRequest(t, h, h.handoff.MCP, sid, "POST", ping, tc.edit)
		if r.status != tc.status || r.value["error"].(map[string]any)["code"] != tc.code {
			t.Fatalf("%s: %d %s", tc.name, r.status, r.body)
		}
		if r.status == 401 && r.header.Get("WWW-Authenticate") != "Bearer" {
			t.Fatal("missing Bearer challenge")
		}
	}
	r = mcpRequest(t, h, h.handoff.Access, sid, "POST", ping, nil)
	wantError(t, r, 401, "UNAUTHENTICATED")
	created := h.do(t, "POST", "/api/v1/admin/credentials", createBody(strings.Repeat("a", 32), "mcp"), nil)
	checkStatus(t, created, 201)
	otherToken := created.value["secret"].(string)
	r = mcpRequest(t, h, otherToken, sid, "POST", "{", nil)
	wantError(t, r, 401, "UNAUTHENTICATED")
	if r.header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("binding mismatch lacks challenge")
	}
	r = mcpRequest(t, h, h.handoff.MCP, sid, "POST", "{", func(r *http.Request) { r.Header.Del("MCP-Protocol-Version") })
	wantError(t, r, 400, "PROTOCOL_VERSION_REQUIRED")
	r = mcpRequest(t, h, h.handoff.MCP, sid, "DELETE", "", nil)
	checkStatus(t, r, 204)
	r = mcpPost(t, h, sid, ping)
	wantError(t, r, 404, "MCP_SESSION_NOT_FOUND")
	sid = mcpInit(t, h)
	clients := h.s.Core.Snapshot().Clients
	var clientID string
	for _, cl := range clients {
		if cl.Lifecycle == "initializing" {
			clientID = cl.ID
		}
	}
	_, err := h.s.Core.EndClient(clientID, "admin_end")
	must(t, err)
	r = mcpPost(t, h, sid, ping)
	wantError(t, r, 404, "MCP_SESSION_NOT_FOUND")
	h.s.mcp.prune()
	h.s.mcp.mu.Lock()
	_, exists := h.s.mcp.bindings[sid]
	h.s.mcp.mu.Unlock()
	if exists {
		t.Fatal("ended transport retained")
	}
	sid = mcpInit(t, h)
	h.f.step(30 * time.Second)
	r = mcpPost(t, h, sid, ping)
	wantError(t, r, 404, "MCP_SESSION_NOT_FOUND")
	sid = mcpInit(t, h)
	mcpReady(t, h, sid)
	h.f.step(30 * time.Minute)
	r = mcpPost(t, h, sid, ping)
	wantError(t, r, 404, "MCP_SESSION_NOT_FOUND")
}

func TestMCPTaskFlow(t *testing.T) {
	h := newWeb(t, nil)
	accepted := make(chan *mcp.CallToolResult, 1)
	release := make(chan struct{})
	defer close(release)
	var intercepted atomic.Bool
	h.s.mcp.server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, request)
			call, ok := request.(*mcp.CallToolRequest)
			toolResult, isTool := result.(*mcp.CallToolResult)
			if err == nil && ok && call.Params.Name == "request_command" && isTool && !toolResult.IsError && intercepted.CompareAndSwap(false, true) {
				accepted <- toolResult
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
				}
			}
			return result, err
		}
	})
	sid := mcpInit(t, h)
	mcpReady(t, h, sid)
	r := agentRequest(t, h, h.handoff.Access, "POST", "/sessions", "{}", nil)
	checkStatus(t, r, 201)
	deviceID, token := r.value["session"].(map[string]any)["id"].(string), r.value["agent_token"].(string)
	path := "/sessions/" + deviceID
	args := object{"session_id": deviceID, "request_id": strings.Repeat("d", 32), "command": "synthetic", "reason": "controlled fixture"}
	body := toolBody(t, h, callTool(t, h, sid, "pending", "request_command", args), "CommandToolBody")
	if body["error"].(map[string]any)["code"] != "LOCAL_CONSENT_REQUIRED" {
		t.Fatal("command bypassed local consent")
	}
	r = agentRequest(t, h, token, "PUT", path+"/decision", `{"decision":"accept","mode":"full"}`, nil)
	checkStatus(t, r, 200)
	body = toolBody(t, h, callTool(t, h, sid, "no-lease", "request_command", args), "CommandToolBody")
	if body["error"].(map[string]any)["code"] != "AGENT_UNAVAILABLE" {
		t.Fatal("command bypassed lease")
	}
	body = toolBody(t, h, callTool(t, h, sid, "sessions", "list_sessions", nil), "SessionsToolBody")
	if len(body["data"].(map[string]any)["sessions"].([]any)) != 1 {
		t.Fatal("session discovery omitted active device")
	}
	body = toolBody(t, h, callTool(t, h, sid, "session", "get_session", object{"session_id": deviceID}), "SessionToolBody")
	if body["data"].(map[string]any)["session"].(map[string]any)["id"] != deviceID {
		t.Fatal("session lookup differs")
	}
	r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
	checkStatus(t, r, 200)
	done := make(chan response, 1)
	go func() { done <- callTool(t, h, sid, "submit", "request_command", args) }()
	var submitted *mcp.CallToolResult
	select {
	case submitted = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("command was not accepted")
	}
	select {
	case <-done:
		t.Fatal("response arrived before cancellation")
	default:
	}
	data := submitted.StructuredContent.(map[string]any)["data"].(map[string]any)
	taskID := data["task"].(map[string]any)["id"].(string)
	if data["accepted"] != true || data["reused"] != false {
		t.Fatal("submit not accepted")
	}
	r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"submit"}}`)
	checkStatus(t, r, 202)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled RPC did not finish")
	}
	body = toolBody(t, h, callTool(t, h, sid, "after-cancel", "get_task", object{"session_id": deviceID, "request_id": args["request_id"]}), "TaskToolBody")
	if body["data"].(map[string]any)["task"].(map[string]any)["id"] != taskID {
		t.Fatal("RPC cancellation lost accepted task")
	}
	body = toolBody(t, h, callTool(t, h, sid, "retry", "request_command", args), "CommandToolBody")
	if data := body["data"].(map[string]any); data["reused"] != true || data["task"].(map[string]any)["id"] != taskID {
		t.Fatal("retry did not reuse intent")
	}
	conflict := object{"session_id": deviceID, "request_id": args["request_id"], "command": "different", "reason": "controlled fixture"}
	body = toolBody(t, h, callTool(t, h, sid, "conflict", "request_command", conflict), "CommandToolBody")
	if body["error"].(map[string]any)["code"] != "IDEMPOTENCY_CONFLICT" {
		t.Fatal("conflicting intent accepted")
	}
	busy := object{"session_id": deviceID, "request_id": strings.Repeat("e", 32), "command": "next", "reason": "controlled fixture"}
	body = toolBody(t, h, callTool(t, h, sid, "busy", "request_command", busy), "CommandToolBody")
	if body["error"].(map[string]any)["code"] != "TASK_BUSY" {
		t.Fatal("concurrent task accepted")
	}
	r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
	checkStatus(t, r, 200)
	events := r.value["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["type"] != "task.offer" {
		t.Fatal("offer missing")
	}
	taskPath := path + "/tasks/" + taskID
	r = agentRequest(t, h, token, "POST", taskPath+"/start", `{"consent_revision":1}`, nil)
	checkStatus(t, r, 200)
	grant := r.value["grant"].(map[string]any)
	grantID := grant["id"].(string)
	if spec := r.value["grant"].(map[string]any)["spec"].(map[string]any); spec["shell"] != "auto" || spec["timeout_seconds"] != float64(30) {
		t.Fatal("command defaults differ")
	}
	r = agentRequest(t, h, token, "POST", taskPath+"/start", `{"consent_revision":1}`, nil)
	checkStatus(t, r, 200)
	if !reflect.DeepEqual(grant, r.value["grant"]) {
		t.Fatal("duplicate start changed the grant")
	}
	started := fmt.Sprintf(`{"kind":"started","grant_id":%q}`, grantID)
	r = agentRequest(t, h, token, "POST", taskPath+"/receipts", started, nil)
	checkStatus(t, r, 200)
	if r.value["task"].(map[string]any)["phase"] != "running" {
		t.Fatal("started receipt did not update task")
	}
	result := fmt.Sprintf(`{"grant_id":%q,"consent_revision":1,"outcome":"succeeded","exit_code":0,"stdout":{"text":"ok","captured_bytes":2,"truncated":false},"stderr":{"text":"","captured_bytes":0,"truncated":false}}`, grantID)
	r = agentRequest(t, h, token, "POST", taskPath+"/result", result, nil)
	checkStatus(t, r, 200)
	if r.value["task"].(map[string]any)["outcome"] != "succeeded" {
		t.Fatal("result did not settle task")
	}
	r = agentRequest(t, h, token, "POST", taskPath+"/result", result, nil)
	checkStatus(t, r, 200)
	body = toolBody(t, h, callTool(t, h, sid, "query", "get_task", object{"session_id": deviceID, "request_id": args["request_id"]}), "TaskToolBody")
	task := body["data"].(map[string]any)["task"].(map[string]any)
	if task["outcome"] != "succeeded" || task["result"].(map[string]any)["stdout"].(map[string]any)["text"] != "ok" {
		t.Fatalf("result lost: %v", task)
	}
	body = toolBody(t, h, callTool(t, h, sid, "tasks", "list_tasks", object{"session_id": deviceID}), "TasksToolBody")
	task = body["data"].(map[string]any)["tasks"].([]any)[0].(map[string]any)
	if _, ok := task["spec"]; ok {
		t.Fatal("task list leaked details")
	}
	body = toolBody(t, h, callTool(t, h, sid, "cancel", "cancel_task", object{"session_id": deviceID, "task_id": taskID}), "CancelToolBody")
	if body["data"].(map[string]any)["task"].(map[string]any)["outcome"] != "succeeded" {
		t.Fatal("late cancellation changed result")
	}
	body = toolBody(t, h, callTool(t, h, sid, "next", "request_command", busy), "CommandToolBody")
	if body["ok"] != true || body["data"].(map[string]any)["accepted"] != true || body["data"].(map[string]any)["reused"] != false {
		t.Fatal("settled task blocked next intent")
	}
	nextID := body["data"].(map[string]any)["task"].(map[string]any)["id"].(string)
	nextPath := path + "/tasks/" + nextID
	r = agentRequest(t, h, token, "POST", nextPath+"/start", `{"consent_revision":1}`, nil)
	checkStatus(t, r, 200)
	nextGrant := r.value["grant"].(map[string]any)["id"].(string)
	r = agentRequest(t, h, token, "POST", nextPath+"/receipts", fmt.Sprintf(`{"kind":"started","grant_id":%q}`, nextGrant), nil)
	checkStatus(t, r, 200)
	body = toolBody(t, h, callTool(t, h, sid, "stop", "cancel_task", object{"session_id": deviceID, "task_id": nextID}), "CancelToolBody")
	stopping := body["data"].(map[string]any)["task"].(map[string]any)
	if stopping["phase"] != "stopping" || stopping["execution"].(map[string]any)["state"] != "started" {
		t.Fatal("cancel falsely reported exit")
	}
	r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
	checkStatus(t, r, 200)
	seenCancel := false
	for _, e := range r.value["events"].([]any) {
		if e.(map[string]any)["type"] == "task.cancel" {
			seenCancel = true
		}
	}
	if !seenCancel {
		t.Fatal("agent did not receive cancellation")
	}
	nextResult := fmt.Sprintf(`{"grant_id":%q,"consent_revision":1,"outcome":"succeeded","exit_code":0,"stdout":{"text":"","captured_bytes":0,"truncated":false},"stderr":{"text":"","captured_bytes":0,"truncated":false}}`, nextGrant)
	r = agentRequest(t, h, token, "POST", nextPath+"/result", nextResult, nil)
	checkStatus(t, r, 200)
	if r.value["task"].(map[string]any)["outcome"] != "cancelled" {
		t.Fatal("late result overrode cancellation")
	}
	r = agentRequest(t, h, token, "POST", path+"/close", `{"reason":"local_end"}`, nil)
	checkStatus(t, r, 200)
	r = agentRequest(t, h, token, "POST", taskPath+"/result", result, nil)
	wantError(t, r, 410, "SESSION_ENDED")
	r = agentRequest(t, h, token, "POST", taskPath+"/receipts", started, nil)
	checkStatus(t, r, 200)
	body = toolBody(t, h, callTool(t, h, sid, "ended", "get_task", object{"session_id": deviceID, "task_id": taskID}), "TaskToolBody")
	task = body["data"].(map[string]any)["task"].(map[string]any)
	if task["details_available"] != false || task["spec"] != nil || task["result"] != nil {
		t.Fatal("ended task retained details")
	}
}

func TestMCPDiagnostics(t *testing.T) {
	h := newWeb(t, nil)
	sid := mcpInit(t, h)
	mcpReady(t, h, sid)
	r := agentRequest(t, h, h.handoff.Access, "POST", "/sessions", "{}", nil)
	deviceID, token := r.value["session"].(map[string]any)["id"].(string), r.value["agent_token"].(string)
	path := "/sessions/" + deviceID
	r = agentRequest(t, h, token, "PUT", path+"/decision", `{"decision":"accept","mode":"readonly"}`, nil)
	checkStatus(t, r, 200)
	r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
	checkStatus(t, r, 200)
	body := toolBody(t, h, callTool(t, h, sid, "readonly-command", "request_command", object{"session_id": deviceID, "request_id": strings.Repeat("f", 32), "command": "synthetic", "reason": "fixture"}), "CommandToolBody")
	if body["error"].(map[string]any)["code"] != "LOCAL_CONSENT_REQUIRED" {
		t.Fatal("read-only session accepted a command")
	}
	for _, tc := range []struct{ name, result, model string }{
		{"ping_agent", `{"pong":true,"agent_time":"2026-11-09T04:00:00.000Z"}`, "PingToolBody"},
		{"get_agent_info", `{"agent_version":"fixture","os":"linux","arch":"amd64","hostname":"test","supported_shells":[]}`, "InfoToolBody"},
	} {
		done := make(chan response, 1)
		go func() { done <- callTool(t, h, sid, tc.name, tc.name, object{"session_id": deviceID}) }()
		r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
		checkStatus(t, r, 200)
		events := r.value["events"].([]any)
		var callID string
		for _, e := range events {
			event := e.(map[string]any)
			if event["type"] == "call.request" && event["payload"].(map[string]any)["method"] == map[string]string{"ping_agent": "ping", "get_agent_info": "info"}[tc.name] {
				callID = event["payload"].(map[string]any)["call_id"].(string)
			}
		}
		if callID == "" {
			t.Fatal("diagnostic request not delivered")
		}
		r = agentRequest(t, h, token, "POST", path+"/calls/"+callID+"/result", `{"consent_revision":1,"result":`+tc.result+`}`, nil)
		checkStatus(t, r, 204)
		select {
		case r = <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("diagnostic did not finish")
		}
		if toolBody(t, h, r, tc.model)["ok"] != true {
			t.Fatalf("diagnostic failed: %s", r.body)
		}
	}
}

func TestMCPDiagnosticCancellationAndTimeout(t *testing.T) {
	for _, name := range []string{"cancel", "timeout", "delete", "admin_end", "shutdown"} {
		t.Run(name, func(t *testing.T) {
			h := newWeb(t, nil)
			sid := mcpInit(t, h)
			mcpReady(t, h, sid)
			r := agentRequest(t, h, h.handoff.Access, "POST", "/sessions", "{}", nil)
			deviceID, token := r.value["session"].(map[string]any)["id"].(string), r.value["agent_token"].(string)
			path := "/sessions/" + deviceID
			r = agentRequest(t, h, token, "PUT", path+"/decision", `{"decision":"accept","mode":"readonly"}`, nil)
			checkStatus(t, r, 200)
			r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
			checkStatus(t, r, 200)
			done := make(chan response, 1)
			go func() { done <- callTool(t, h, sid, "waiting", "ping_agent", object{"session_id": deviceID}) }()
			r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
			checkStatus(t, r, 200)
			if len(r.value["events"].([]any)) == 0 {
				t.Fatal("diagnostic not sent")
			}
			switch name {
			case "cancel":
				r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"waiting"}}`)
				checkStatus(t, r, 202)
			case "timeout":
				h.f.step(10 * time.Second)
				h.s.Core.Sweep()
			case "delete":
				r = mcpRequest(t, h, h.handoff.MCP, sid, "DELETE", "", nil)
				checkStatus(t, r, 204)
			case "admin_end":
				h.s.mcp.mu.Lock()
				clientID := h.s.mcp.bindings[sid].clientID
				h.s.mcp.mu.Unlock()
				_, err := h.s.Core.EndClient(clientID, "admin_end")
				must(t, err)
				h.s.mcp.prune()
			case "shutdown":
				h.s.Core.Halt("shutting_down")
			}
			select {
			case r = <-done:
				if name == "timeout" {
					body := toolBody(t, h, r, "PingToolBody")
					if body["error"].(map[string]any)["code"] != "DIAGNOSTIC_TIMEOUT" {
						t.Fatal("diagnostic timeout not propagated")
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatal("diagnostic wait did not stop")
			}
		})
	}
}

func TestMCPCredentialRevocation(t *testing.T) {
	h := newWeb(t, nil)
	sid := mcpInit(t, h)
	mcpReady(t, h, sid)
	id, err := h.s.Core.AuthenticateCredential(h.handoff.MCP, "mcp")
	must(t, err)
	r := h.do(t, "POST", "/api/v1/admin/credentials/"+id+"/revoke", "{}", nil)
	checkStatus(t, r, 200)
	r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":"after","method":"ping"}`)
	wantError(t, r, 401, "UNAUTHENTICATED")
	if r.header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("revoked bearer lacks challenge")
	}
	h.s.mcp.prune()
	h.s.mcp.mu.Lock()
	_, exists := h.s.mcp.bindings[sid]
	h.s.mcp.mu.Unlock()
	if exists {
		t.Fatal("revoked transport retained")
	}
}

func TestMCPConcurrentRPCLimit(t *testing.T) {
	h := newWeb(t, nil)
	h.web.Client().Timeout = 8 * time.Second
	sid := mcpInit(t, h)
	mcpReady(t, h, sid)
	r := agentRequest(t, h, h.handoff.Access, "POST", "/sessions", "{}", nil)
	deviceID, token := r.value["session"].(map[string]any)["id"].(string), r.value["agent_token"].(string)
	path := "/sessions/" + deviceID
	r = agentRequest(t, h, token, "PUT", path+"/decision", `{"decision":"accept","mode":"readonly"}`, nil)
	checkStatus(t, r, 200)
	r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
	checkStatus(t, r, 200)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 16)
	for i := 0; i < 16; i++ {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"waiting-%d","method":"tools/call","params":{"name":"ping_agent","arguments":{"session_id":%q}}}`, i, deviceID)
		req, err := http.NewRequestWithContext(ctx, "POST", h.web.URL+"/mcp", strings.NewReader(body))
		must(t, err)
		req.Header.Set("Authorization", "Bearer "+h.handoff.MCP)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Session-Id", sid)
		req.Header.Set("MCP-Protocol-Version", mcpVersion)
		go func() {
			resp, err := h.web.Client().Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			done <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	var events []core.Event
	for time.Now().Before(deadline) {
		poll, err := h.s.Core.BeginPoll(deviceID, 1, nil)
		must(t, err)
		result, err := h.s.Core.CompletePoll(poll)
		must(t, err)
		events = result.Events
		if len(events) == 16 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(events) != 16 {
		t.Fatalf("only %d concurrent diagnostics started", len(events))
	}
	r = mcpPost(t, h, sid, `{"jsonrpc":"2.0","id":"over-limit","method":"ping"}`)
	wantError(t, r, 503, "CAPACITY_EXCEEDED")
	cancel()
	for i := 0; i < 16; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("request did not disconnect: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("HTTP request did not disconnect")
		}
	}
	// Disconnected HTTP clients do not end the SDK handlers. Keep their slots
	// until those handlers actually finish, not just until their sockets close.
	deadline = time.Now().Add(500 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		r = mcpPost(t, h, sid, fmt.Sprintf(`{"jsonrpc":"2.0","id":"after-disconnect-%d","method":"ping"}`, i))
		wantError(t, r, 503, "CAPACITY_EXCEEDED")
		time.Sleep(10 * time.Millisecond)
	}
	r = agentRequest(t, h, token, "POST", path+"/calls/"+events[0].CallID+"/result", `{"consent_revision":1,"result":{"pong":true,"agent_time":"2026-11-09T04:00:00.000Z"}}`, nil)
	checkStatus(t, r, 204)
	deadline = time.Now().Add(2 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		r = mcpPost(t, h, sid, fmt.Sprintf(`{"jsonrpc":"2.0","id":"after-completion-%d","method":"ping"}`, i))
		if r.status == 200 {
			break
		}
		wantError(t, r, 503, "CAPACITY_EXCEEDED")
		time.Sleep(10 * time.Millisecond)
	}
	checkStatus(t, r, 200)
	// Ending the client must still wake all other in-flight diagnostics.
	r = mcpRequest(t, h, h.handoff.MCP, sid, "DELETE", "", nil)
	checkStatus(t, r, 204)
}
