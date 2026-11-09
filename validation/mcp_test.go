package validation

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const protocolVersion = "2025-11-25"

// This loopback-only fixture adapter probes SDK extension points, not Remota business state.
// The eight tools return canned data/errors and never register devices or execute commands.
type mcpProbe struct {
	url      string
	client   *http.Client
	server   *mcp.Server
	mu       sync.Mutex
	bindings map[string]string
	ready    map[string]bool
	revoked  atomic.Bool
	input    map[string]*jsonschema.Schema
	models   map[string]*jsonschema.Schema
}

func newMCPProbe(t *testing.T, hook func(context.Context, string)) *mcpProbe {
	t.Helper()
	p := &mcpProbe{client: &http.Client{Timeout: 5 * time.Second}, bindings: map[string]string{}, ready: map[string]bool{}, input: map[string]*jsonschema.Schema{}, models: map[string]*jsonschema.Schema{}}
	c := schemaCompiler(t)
	for _, name := range []string{"MCPRequest", "MCPToolRequest", "MCPInitializeResponse", "MCPToolsListResponse", "MCPToolResponse"} {
		s, err := c.Compile("urn:remota:draft2:messages#/$defs/" + name)
		if err != nil {
			t.Fatal(err)
		}
		p.models[name] = s
	}
	p.server = mcp.NewServer(&mcp.Implementation{Name: "remota-probe", Version: "0.0.0"}, &mcp.ServerOptions{
		SupportedProtocolVersions: []string{protocolVersion},
		Capabilities:              &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: false}},
		InitializedHandler: func(_ context.Context, r *mcp.InitializedRequest) {
			p.mu.Lock()
			p.ready[r.Session.ID()] = true
			p.mu.Unlock()
		},
	})
	var listing struct {
		Result struct {
			Tools []jsontext.Value `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(contractFile(t, "acceptance/tools-list.json"), &listing); err != nil {
		t.Fatal(err)
	}
	for _, raw := range listing.Result.Tools {
		var tool mcp.Tool
		if err := json.Unmarshal(raw, &tool); err != nil {
			t.Fatal(err)
		}
		var item struct {
			InputSchema jsontext.Value `json:"inputSchema"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		inputValue, err := strictJSON(item.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		inputURL := "urn:remota:probe:input:" + tool.Name
		if err := c.AddResource(inputURL, inputValue); err != nil {
			t.Fatal(err)
		}
		compiled, err := c.Compile(inputURL)
		if err != nil {
			t.Fatal(err)
		}
		p.input[tool.Name] = compiled
		name := tool.Name
		p.server.AddTool(&tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := []byte(req.Params.Arguments)
			if len(args) == 0 {
				args = []byte("{}")
			}
			value, err := strictJSON(args)
			if err != nil {
				return probeToolError("INVALID_ARGUMENT"), nil
			}
			if err := compiled.Validate(value); err != nil {
				code := "INVALID_ARGUMENT"
				// The no-argument tool has an empty property set: extra keys are unambiguously UNKNOWN_FIELD.
				if name == "list_sessions" {
					code = "UNKNOWN_FIELD"
				}
				return probeToolError(code), nil
			}
			if hook != nil {
				hook(ctx, name)
			}
			if name == "list_sessions" {
				return probeToolResult(map[string]any{"ok": true, "data": map[string]any{"sessions": []any{}}}), nil
			}
			return probeToolError("LOCAL_CONSENT_REQUIRED"), nil
		})
	}
	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return p.server }, &mcp.StreamableHTTPOptions{JSONResponse: true, MaxRequestBodyBytes: 1 << 20})
	authenticated := auth.RequireBearerToken(func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if p.revoked.Load() || (token != "probe-a" && token != "probe-b") {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: token, Expiration: time.Now().Add(time.Minute)}, nil
	}, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.profile(w, r, sdk) }))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rr := httptest.NewRecorder()
		authenticated.ServeHTTP(rr, r)
		if rr.Code >= 400 && !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") {
			code := "INVALID_ARGUMENT"
			if rr.Code == 401 {
				code = "UNAUTHENTICATED"
			}
			if rr.Code == 403 {
				code = "HOST_DENIED"
			}
			if rr.Code == 404 {
				code = "MCP_SESSION_NOT_FOUND"
			}
			if rr.Code == 401 {
				w.Header().Set("WWW-Authenticate", "Bearer")
			}
			probeHTTPError(w, rr.Code, code)
			return
		}
		for key, values := range rr.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(rr.Code)
		_, _ = w.Write(rr.Body.Bytes())
	}))
	p.url = srv.URL
	t.Cleanup(func() {
		for s := range p.server.Sessions() {
			_ = s.Close()
		}
		srv.Close()
	})
	return p
}

func probeToolResult(body map[string]any) *mcp.CallToolResult {
	b, _ := json.Marshal(body)
	ok, _ := body["ok"].(bool)
	return &mcp.CallToolResult{StructuredContent: body, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, IsError: !ok}
}
func probeToolError(code string) *mcp.CallToolResult {
	return probeToolResult(map[string]any{"ok": false, "error": map[string]any{"code": code, "message": "fixture rejection", "trace_id": strings.Repeat("a", 32)}})
}
func probeWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}
func probeHTTPError(w http.ResponseWriter, status int, code string) {
	if status == 401 {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	probeWriteJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": "fixture rejection", "trace_id": strings.Repeat("a", 32)}})
}
func probeRPCError(w http.ResponseWriter, status int, id jsontext.Value, code int, data any) {
	if len(id) == 0 {
		id = jsontext.Value("null")
	}
	e := map[string]any{"code": code, "message": "fixture rejection"}
	if data != nil {
		e["data"] = data
	}
	probeWriteJSON(w, status, map[string]any{"jsonrpc": "2.0", "id": id, "error": e})
}

func (p *mcpProbe) profile(w http.ResponseWriter, r *http.Request, sdk http.Handler) {
	if _, ok := r.Header["Origin"]; ok {
		probeHTTPError(w, 403, "ORIGIN_DENIED")
		return
	}
	sid := r.Header.Get("Mcp-Session-Id")
	token := auth.TokenInfoFromContext(r.Context()).UserID
	var raw []byte
	var msg map[string]jsontext.Value
	var method string
	if r.Method == "POST" {
		var err error
		raw, err = io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 {
			probeHTTPError(w, 413, "PAYLOAD_TOO_LARGE")
			return
		}
		if _, err := strictJSON(raw); err != nil {
			probeRPCError(w, 400, nil, -32700, nil)
			return
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			probeRPCError(w, 400, nil, -32600, nil)
			return
		}
		_ = json.Unmarshal(msg["method"], &method)
		if !strings.HasPrefix(method, "notifications/") {
			v, _ := strictJSON(raw)
			if p.models["MCPRequest"].Validate(v) != nil {
				probeRPCError(w, 400, nil, -32600, nil)
				return
			}
		}
	}
	if method == "initialize" {
		if sid != "" {
			probeHTTPError(w, 400, "INVALID_ARGUMENT")
			return
		}
	} else {
		if sid == "" {
			probeHTTPError(w, 400, "INVALID_ARGUMENT")
			return
		}
		if r.Header.Get("MCP-Protocol-Version") == "" {
			probeHTTPError(w, 400, "PROTOCOL_VERSION_REQUIRED")
			return
		}
		if r.Header.Get("MCP-Protocol-Version") != protocolVersion {
			probeHTTPError(w, 400, "PROTOCOL_VERSION_UNSUPPORTED")
			return
		}
		p.mu.Lock()
		bound, exists := p.bindings[sid]
		ready := p.ready[sid]
		p.mu.Unlock()
		if !exists {
			probeHTTPError(w, 404, "MCP_SESSION_NOT_FOUND")
			return
		}
		if bound != token {
			probeHTTPError(w, 401, "UNAUTHENTICATED")
			return
		}
		if r.Method == "GET" {
			w.Header().Set("Allow", "POST, DELETE")
			w.WriteHeader(405)
			return
		}
		if method == "tools/call" || method == "tools/list" {
			if !ready {
				probeRPCError(w, 200, msg["id"], -32000, map[string]any{"code": "MCP_NOT_INITIALIZED"})
				return
			}
		}
	}
	if r.Method == "POST" {
		switch method {
		case "initialize", "ping", "tools/list", "notifications/initialized", "notifications/cancelled":
		case "tools/call":
			v, _ := strictJSON(raw)
			if p.models["MCPToolRequest"].Validate(v) != nil {
				probeRPCError(w, 200, msg["id"], -32602, nil)
				return
			}
			var params map[string]jsontext.Value
			_ = json.Unmarshal(msg["params"], &params)
			var name string
			_ = json.Unmarshal(params["name"], &name)
			if p.input[name] == nil || params["task"] != nil {
				probeRPCError(w, 200, msg["id"], -32602, nil)
				return
			}
		default:
			probeRPCError(w, 200, msg["id"], -32601, nil)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	rr := httptest.NewRecorder()
	sdk.ServeHTTP(rr, r)
	if method == "initialize" && rr.Code == 200 {
		newSID := rr.Header().Get("Mcp-Session-Id")
		p.mu.Lock()
		p.bindings[newSID] = token
		p.mu.Unlock()
		// SDK uses omitempty for false. The profile explicitly emits the contract's false value.
		var body map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		result, _ := body["result"].(map[string]any)
		if result != nil {
			caps, _ := result["capabilities"].(map[string]any)
			if caps != nil {
				if tools, ok := caps["tools"].(map[string]any); ok {
					tools["listChanged"] = false
				}
			}
		}
		b, _ := json.Marshal(body)
		rr.Body.Reset()
		_, _ = rr.Body.Write(b)
	}
	if method == "tools/list" && rr.Code == 200 {
		// SDK v1.8.0 has no Tool.Execution field. Add only this profile field;
		// the test still compares every SDK-provided definition with the contract.
		var body, result map[string]jsontext.Value
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		_ = json.Unmarshal(body["result"], &result)
		var tools []map[string]jsontext.Value
		_ = json.Unmarshal(result["tools"], &tools)
		for _, tool := range tools {
			tool["execution"] = jsontext.Value(`{"taskSupport":"forbidden"}`)
		}
		result["tools"], _ = json.Marshal(tools)
		body["result"], _ = json.Marshal(result)
		b, _ := json.Marshal(body)
		rr.Body.Reset()
		_, _ = rr.Body.Write(b)
	}
	if method == "tools/call" && rr.Code == 200 {
		// SDK also omits isError=false; the contract requires it explicitly.
		var body, result map[string]jsontext.Value
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		if json.Unmarshal(body["result"], &result) == nil && result != nil {
			if result["isError"] == nil {
				result["isError"] = jsontext.Value("false")
			}
			body["result"], _ = json.Marshal(result)
			b, _ := json.Marshal(body)
			rr.Body.Reset()
			_, _ = rr.Body.Write(b)
		}
	}
	if r.Method == "DELETE" && rr.Code == 204 {
		p.mu.Lock()
		delete(p.bindings, sid)
		delete(p.ready, sid)
		p.mu.Unlock()
	}
	for key, values := range rr.Header() {
		w.Header()[key] = values
	}
	w.WriteHeader(rr.Code)
	_, _ = w.Write(rr.Body.Bytes())
}

type probeResponse struct {
	status int
	header http.Header
	body   []byte
}

func (p *mcpProbe) request(t *testing.T, method, sid, token, version string, body []byte) probeResponse {
	t.Helper()
	r, err := http.NewRequest(method, p.url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if sid != "" {
		r.Header.Set("Mcp-Session-Id", sid)
	}
	if version != "" {
		r.Header.Set("MCP-Protocol-Version", version)
	}
	resp, err := p.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return probeResponse{resp.StatusCode, resp.Header, b}
}
func (p *mcpProbe) post(t *testing.T, sid, id, method string, params any) probeResponse {
	t.Helper()
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != "" {
		m["id"] = id
	}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return p.request(t, "POST", sid, "probe-a", protocolVersion, b)
}
func (p *mcpProbe) initialize(t *testing.T, version string) string {
	t.Helper()
	r := p.post(t, "", "init", "initialize", map[string]any{"protocolVersion": version, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "probe", "version": "0"}})
	if r.status != 200 {
		t.Fatalf("initialize: %d %s", r.status, r.body)
	}
	v, err := strictJSON(r.body)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.models["MCPInitializeResponse"].Validate(v); err != nil {
		t.Fatal(err)
	}
	sid := r.header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("missing transport session id")
	}
	return sid
}
func (p *mcpProbe) initialized(t *testing.T, sid string) {
	t.Helper()
	r := p.post(t, sid, "", "notifications/initialized", map[string]any{"_meta": map[string]any{}})
	if r.status != 202 || len(r.body) != 0 {
		t.Fatalf("notification: %d %s", r.status, r.body)
	}
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		p.mu.Lock()
		ok := p.ready[sid]
		p.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("initialized callback not observed")
}
func rpcErrorCode(t *testing.T, r probeResponse) int {
	t.Helper()
	var b struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.body, &b); err != nil {
		t.Fatal(err)
	}
	return b.Error.Code
}

func TestMCPProfileHandshakeDiscoveryAndErrors(t *testing.T) {
	p := newMCPProbe(t, nil)
	sid := p.initialize(t, "unsupported-version")
	r := p.post(t, sid, "early", "tools/list", nil)
	if rpcErrorCode(t, r) != -32000 {
		t.Fatal("ready gate not applied")
	}
	p.initialized(t, sid)
	r = p.post(t, sid, "tools", "tools/list", nil)
	v, err := strictJSON(r.body)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.models["MCPToolsListResponse"].Validate(v); err != nil {
		t.Fatal(err)
	}
	var got, want struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	_ = json.Unmarshal(r.body, &got)
	_ = json.Unmarshal(contractFile(t, "acceptance/tools-list.json"), &want)
	byName := map[string]map[string]any{}
	for _, tool := range got.Result.Tools {
		byName[tool["name"].(string)] = tool
	}
	for _, tool := range want.Result.Tools {
		if !reflect.DeepEqual(byName[tool["name"].(string)], tool) {
			t.Fatalf("tool definition differs: %s", tool["name"])
		}
	}
	for name, params := range map[string]any{"omitted": map[string]any{"name": "list_sessions"}, "extra": map[string]any{"name": "list_sessions", "arguments": map[string]any{"extra": true}}, "missing": map[string]any{"name": "get_session"}} {
		r = p.post(t, sid, name, "tools/call", params)
		if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
			t.Fatalf("tool HTTP: %d", r.status)
		}
		v, err := strictJSON(r.body)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.models["MCPToolResponse"].Validate(v); err != nil {
			t.Fatal(err)
		}
		var response struct {
			Result struct {
				StructuredContent map[string]any `json:"structuredContent"`
				Content           []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		_ = json.Unmarshal(r.body, &response)
		var text map[string]any
		_ = json.Unmarshal([]byte(response.Result.Content[0].Text), &text)
		if !reflect.DeepEqual(text, response.Result.StructuredContent) {
			t.Fatal("tool text mismatch")
		}
		if response.Result.IsError != (name != "omitted") {
			t.Fatal("wrong business error layer")
		}
	}
	for i, params := range []any{map[string]any{"name": "unknown"}, map[string]any{"name": "list_sessions", "task": map[string]any{}}, map[string]any{"name": "list_sessions", "arguments": nil}} {
		if r = p.post(t, sid, "bad-"+strconv.Itoa(i), "tools/call", params); rpcErrorCode(t, r) != -32602 {
			t.Fatal("wrong protocol error")
		}
	}
	r = p.request(t, "POST", sid, "probe-a", protocolVersion, []byte(`{"jsonrpc":"2.0","id":1.5,"method":"ping"}`))
	if r.status != 400 || rpcErrorCode(t, r) != -32600 {
		t.Fatal("fractional RPC id accepted")
	}
	r = p.post(t, sid, "unknown", "server/discover", nil)
	if rpcErrorCode(t, r) != -32601 {
		t.Fatal("unadvertised method exposed")
	}
}

func TestMCPTransportBindingAndTermination(t *testing.T) {
	p := newMCPProbe(t, nil)
	sid := p.initialize(t, protocolVersion)
	p.initialized(t, sid)
	for _, tc := range []struct {
		method, token, version string
		status                 int
	}{{"GET", "probe-a", protocolVersion, 405}, {"GET", "probe-b", protocolVersion, 401}, {"POST", "invalid", protocolVersion, 401}, {"POST", "probe-a", "", 400}, {"POST", "probe-a", "wrong", 400}} {
		r := p.request(t, tc.method, sid, tc.token, tc.version, []byte(`{"jsonrpc":"2.0","id":"ping","method":"ping"}`))
		if r.status != tc.status {
			t.Fatalf("binding %s: %d", tc.method, r.status)
		}
		if r.status == 401 && r.header.Get("WWW-Authenticate") != "Bearer" {
			t.Fatal("missing authentication challenge")
		}
	}
	r := p.request(t, "DELETE", sid, "probe-a", protocolVersion, nil)
	if r.status != 204 {
		t.Fatal("DELETE did not end transport")
	}
	r = p.post(t, sid, "after", "ping", nil)
	if r.status != 404 {
		t.Fatal("deleted transport revived")
	}
	sid = p.initialize(t, protocolVersion)
	p.revoked.Store(true)
	r = p.post(t, sid, "revoked", "ping", nil)
	if r.status != 401 {
		t.Fatal("cached session bypassed verifier")
	}
}

func TestMCPCancellationContextBoundary(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	p := newMCPProbe(t, func(ctx context.Context, name string) {
		if name == "ping_agent" {
			close(started)
			<-ctx.Done()
			close(stopped)
		}
	})
	sid := p.initialize(t, protocolVersion)
	p.initialized(t, sid)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.post(t, sid, "waiting", "tools/call", map[string]any{"name": "ping_agent", "arguments": map[string]any{"session_id": "ses_" + strings.Repeat("1", 32)}})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool not called")
	}
	r := p.post(t, sid, "", "notifications/cancelled", map[string]any{"requestId": "waiting"})
	if r.status != 202 {
		t.Fatal("cancel notification rejected")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("SDK did not cancel RPC context")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RPC remained stuck")
	}
	t.Log("SDK cancels the RPC context; future accepted tasks must have a core-owned lifetime, not this context")
}
