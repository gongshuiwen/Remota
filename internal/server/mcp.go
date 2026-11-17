package server

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	json "encoding/json/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"remota.local/remota/internal/protocol"
	"remota.local/remota/internal/server/core"
)

const mcpVersion = "2025-11-25"
const mcpMaxBody = 1 << 20

type mcpBinding struct {
	credentialID, clientID string
	rpc                    chan struct{}
}

type mcpGateway struct {
	owner     *Server
	server    *mcp.Server
	transport http.Handler
	mu        sync.Mutex
	bindings  map[string]*mcpBinding
	tools     map[string]toolDefinition
}

func newMCPGateway(owner *Server) (*mcpGateway, error) {
	g := &mcpGateway{owner: owner, bindings: map[string]*mcpBinding{}, tools: map[string]toolDefinition{}}
	g.server = mcp.NewServer(&mcp.Implementation{Name: "remota-server", Version: Version}, &mcp.ServerOptions{
		SupportedProtocolVersions: []string{mcpVersion},
		Capabilities:              &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: false}},
	})
	if err := g.registerTools(); err != nil {
		return nil, err
	}
	g.transport = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return g.server }, &mcp.StreamableHTTPOptions{
		JSONResponse: true, MaxRequestBodyBytes: mcpMaxBody,
		// The outer server enforces the configured authority, including behind a proxy.
		DisableLocalhostProtection: true,
	})
	return g, nil
}

func (g *mcpGateway) fail(w http.ResponseWriter, err error) {
	if err == core.Unauthenticated {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	g.owner.fail(w, err)
}

func (g *mcpGateway) rpcError(w http.ResponseWriter, status int, id any, code int, data any) {
	e := object{"code": code, "message": "JSON-RPC error"}
	if data != nil {
		e["data"] = data
	}
	g.owner.respond(w, status, object{"jsonrpc": "2.0", "id": id, "error": e})
}

func mcpMedia(r *http.Request) error {
	if err := jsonMedia(r); err != nil {
		return err
	}
	jsonOK, streamOK := false, false
	for _, header := range r.Header.Values("Accept") {
		for _, item := range strings.Split(header, ",") {
			media, p, err := mime.ParseMediaType(strings.TrimSpace(item))
			if err != nil {
				continue
			}
			if quality := p["q"]; quality != "" {
				q, err := strconv.ParseFloat(quality, 64)
				if err != nil || !(q > 0 && q <= 1) {
					continue
				}
			}
			jsonOK = jsonOK || media == "application/json"
			streamOK = streamOK || media == "text/event-stream"
		}
	}
	if !jsonOK || !streamOK {
		return core.InvalidArgument
	}
	return nil
}

func (g *mcpGateway) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.EscapedPath() != "/mcp" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		g.fail(w, core.InvalidArgument)
		return
	}
	if len(r.Header.Values("Origin")) != 0 {
		g.fail(w, core.Code("ORIGIN_DENIED"))
		return
	}
	token, err := agentBearer(r)
	if err != nil {
		g.fail(w, err)
		return
	}
	credentialID, err := g.owner.Core.AuthenticateCredential(token, "mcp")
	if err != nil {
		g.fail(w, err)
		return
	}
	if r.Method != "POST" && r.Method != "GET" && r.Method != "DELETE" {
		w.Header().Set("Allow", "POST, DELETE")
		g.owner.respond(w, 405, object{"error": object{"code": "INVALID_ARGUMENT", "message": "method not allowed", "trace_id": w.Header().Get("X-Request-ID")}})
		return
	}
	if r.Method != "POST" && (r.ContentLength > 0 || len(r.TransferEncoding) > 0) {
		g.fail(w, core.InvalidArgument)
		return
	}
	// Validate an existing transport before parsing its message, so malformed
	// input cannot bypass session binding or version checks.
	sidValues := r.Header.Values("Mcp-Session-Id")
	var sid string
	var binding *mcpBinding
	var cl core.Client
	if len(sidValues) > 1 || len(sidValues) == 1 && sidValues[0] == "" {
		g.fail(w, core.InvalidArgument)
		return
	}
	if len(sidValues) == 1 {
		versions := r.Header.Values("MCP-Protocol-Version")
		if len(versions) != 1 || versions[0] == "" {
			g.fail(w, core.Code("PROTOCOL_VERSION_REQUIRED"))
			return
		}
		if versions[0] != mcpVersion {
			g.fail(w, core.Code("PROTOCOL_VERSION_UNSUPPORTED"))
			return
		}
		sid = sidValues[0]
		g.mu.Lock()
		binding = g.bindings[sid]
		g.mu.Unlock()
		if binding == nil {
			g.fail(w, core.ClientNotFound)
			return
		}
		if binding.credentialID != credentialID {
			g.fail(w, core.Unauthenticated)
			return
		}
		cl, err = g.owner.Core.ClientStatus(credentialID, binding.clientID)
		if err != nil {
			g.fail(w, err)
			return
		}
	} else if r.Method != "POST" {
		g.fail(w, core.InvalidArgument)
		return
	}

	var raw []byte
	var message object
	var method string
	var notification bool
	if r.Method == "POST" {
		if err := mcpMedia(r); err != nil {
			g.fail(w, err)
			return
		}
		if r.ContentLength > mcpMaxBody {
			g.fail(w, core.Code("PAYLOAD_TOO_LARGE"))
			return
		}
		raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, mcpMaxBody))
		if err != nil {
			g.fail(w, core.Code("PAYLOAD_TOO_LARGE"))
			return
		}
		value, err := protocol.Parse(raw)
		if err != nil {
			g.rpcError(w, 400, nil, -32700, nil)
			return
		}
		message, _ = value.(map[string]any)
		if message == nil {
			g.rpcError(w, 400, nil, -32600, nil)
			return
		}
		method, _ = message["method"].(string)
		_, hasID := message["id"]
		notification = !hasID
		if hasID && strings.HasPrefix(method, "notifications/") {
			g.rpcError(w, 400, nil, -32600, nil)
			return
		}
		if notification {
			_, hasResult := message["result"]
			_, hasError := message["error"]
			params, hasParams := message["params"]
			_, paramsObject := params.(map[string]any)
			if message["jsonrpc"] != "2.0" || method == "" || hasResult || hasError || hasParams && !paramsObject {
				g.rpcError(w, 400, nil, -32600, nil)
				return
			}
		} else if g.owner.validator.Validate("MCPRequest", value) != nil {
			g.rpcError(w, 400, nil, -32600, nil)
			return
		}
	}

	if method == "initialize" && !notification {
		if len(sidValues) != 0 {
			g.fail(w, core.InvalidArgument)
			return
		}
		if g.owner.validator.Validate("MCPInitialize", message) != nil {
			g.rpcError(w, 200, message["id"], -32602, nil)
			return
		}
		params := message["params"].(map[string]any)
		info := params["clientInfo"].(map[string]any)
		if len(info["name"].(string)) > 80 || len(info["version"].(string)) > 40 {
			g.rpcError(w, 200, message["id"], -32602, nil)
			return
		}
		cl, err := g.owner.Core.BeginClient(credentialID, core.ClientInfo{Name: info["name"].(string), Version: info["version"].(string)})
		if err != nil {
			g.fail(w, err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		rr := httptest.NewRecorder()
		g.transport.ServeHTTP(rr, r)
		newSID := rr.Header().Get("Mcp-Session-Id")
		value, parseErr := protocol.Parse(rr.Body.Bytes())
		body, _ := value.(map[string]any)
		_, hasResult := body["result"].(map[string]any)
		if rr.Code != 200 || newSID == "" || parseErr != nil || !hasResult {
			_, _ = g.owner.Core.EndClient(cl.ID, "client_delete")
			if newSID != "" {
				for session := range g.server.Sessions() {
					if session.ID() == newSID {
						_ = session.Close()
					}
				}
			}
			if rr.Code == 200 {
				g.rpcError(w, 200, message["id"], -32603, nil)
				return
			}
			g.sdkFailure(w, rr.Code)
			return
		}
		g.mu.Lock()
		g.bindings[newSID] = &mcpBinding{credentialID: credentialID, clientID: cl.ID, rpc: make(chan struct{}, 16)}
		g.mu.Unlock()
		g.writeSDK(w, rr, method, message["id"])
		return
	}
	if binding == nil {
		g.fail(w, core.InvalidArgument)
		return
	}
	if r.Method == "GET" {
		w.Header().Set("Allow", "POST, DELETE")
		g.owner.respond(w, 405, object{"error": object{"code": "INVALID_ARGUMENT", "message": "method not allowed", "trace_id": w.Header().Get("X-Request-ID")}})
		return
	}
	if r.Method == "DELETE" {
		// End the core client first so its outstanding diagnostic waits wake
		// before SDK Close waits for in-flight handlers to finish.
		if _, err := g.owner.Core.EndClient(binding.clientID, "client_delete"); err != nil {
			g.fail(w, err)
			return
		}
		rr := httptest.NewRecorder()
		g.transport.ServeHTTP(rr, r)
		if rr.Code != 204 {
			g.sdkFailure(w, rr.Code)
			return
		}
		g.mu.Lock()
		delete(g.bindings, sid)
		g.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	if notification {
		if method == "notifications/initialized" && g.owner.validator.Validate("MCPInitialized", message) != nil {
			g.rpcError(w, 400, nil, -32600, nil)
			return
		}
		ignore := method != "notifications/initialized" && method != "notifications/cancelled"
		if method == "notifications/cancelled" {
			params, _ := message["params"].(map[string]any)
			requestID, exists := params["requestId"]
			reason, hasReason := params["reason"]
			_, reasonOK := reason.(string)
			ignore = !exists || g.owner.validator.Validate("RPCId", requestID) != nil || hasReason && !reasonOK
		}
		if ignore {
			if cl.Lifecycle == "ready" {
				if _, err := g.owner.Core.ClientMessage(credentialID, binding.clientID, false); err != nil {
					g.fail(w, err)
					return
				}
			}
			w.WriteHeader(202)
			return
		}
	} else {
		if cl.Lifecycle != "ready" && (method == "tools/list" || method == "tools/call") {
			g.rpcError(w, 200, message["id"], -32000, object{"code": "MCP_NOT_INITIALIZED"})
			return
		}
		switch method {
		case "ping":
			if params, ok := message["params"].(map[string]any); ok {
				for key, value := range params {
					_, meta := value.(map[string]any)
					if key != "_meta" || !meta {
						g.rpcError(w, 200, message["id"], -32602, nil)
						return
					}
				}
			}
		case "tools/list":
			if g.owner.validator.Validate("MCPToolsListRequest", message) != nil {
				g.rpcError(w, 200, message["id"], -32602, nil)
				return
			}
			if params, ok := message["params"].(map[string]any); ok {
				if _, exists := params["cursor"]; exists {
					g.rpcError(w, 200, message["id"], -32602, nil)
					return
				}
			}
		case "tools/call":
			if g.owner.validator.Validate("MCPToolRequest", message) != nil {
				g.rpcError(w, 200, message["id"], -32602, nil)
				return
			}
			params := message["params"].(map[string]any)
			name := params["name"].(string)
			if _, exists := g.tools[name]; !exists {
				g.rpcError(w, 200, message["id"], -32602, nil)
				return
			}
			if _, exists := params["task"]; exists {
				g.rpcError(w, 200, message["id"], -32602, nil)
				return
			}
		default:
			g.rpcError(w, 200, message["id"], -32601, nil)
			return
		}
		select {
		case binding.rpc <- struct{}{}:
			defer func() { <-binding.rpc }()
		default:
			g.fail(w, core.CapacityExceeded)
			return
		}
	}
	if method == "notifications/initialized" || cl.Lifecycle == "ready" {
		if _, err := g.owner.Core.ClientMessage(credentialID, binding.clientID, method == "notifications/initialized"); err != nil {
			g.fail(w, err)
			return
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	requestContext := r.Context()
	if !notification {
		if requestContext.Err() != nil {
			return
		}
		// The SDK keeps 2025-11-25 RPC handlers alive after an HTTP disconnect.
		// Keep the admission slot until that handler has actually completed.
		r = r.WithContext(context.WithoutCancel(requestContext))
	}
	g.transport.ServeHTTP(rr, r)
	if requestContext.Err() != nil {
		return
	}
	if rr.Code >= 400 {
		g.sdkFailure(w, rr.Code)
		return
	}
	g.writeSDK(w, rr, method, message["id"])
}

func (g *mcpGateway) sdkFailure(w http.ResponseWriter, status int) {
	switch status {
	case 401:
		g.fail(w, core.Unauthenticated)
	case 404:
		g.fail(w, core.ClientNotFound)
	case 413:
		g.fail(w, core.Code("PAYLOAD_TOO_LARGE"))
	case 415:
		g.fail(w, core.Code("UNSUPPORTED_MEDIA_TYPE"))
	case 400:
		g.fail(w, core.InvalidArgument)
	default:
		g.fail(w, core.Code("INTERNAL_ERROR"))
	}
}

func (g *mcpGateway) writeSDK(w http.ResponseWriter, rr *httptest.ResponseRecorder, method string, id any) {
	for key, values := range rr.Header() {
		if !strings.EqualFold(key, "Cache-Control") {
			w.Header()[key] = values
		}
	}
	if rr.Code == 202 || rr.Code == 204 {
		w.WriteHeader(rr.Code)
		return
	}
	value, err := protocol.Parse(rr.Body.Bytes())
	if err != nil {
		g.rpcError(w, 200, id, -32603, nil)
		return
	}
	body, ok := value.(map[string]any)
	if !ok {
		g.rpcError(w, 200, id, -32603, nil)
		return
	}
	body["id"] = id
	result, _ := body["result"].(map[string]any)
	if result != nil {
		switch method {
		case "initialize":
			if caps, ok := result["capabilities"].(map[string]any); ok {
				if tools, ok := caps["tools"].(map[string]any); ok {
					tools["listChanged"] = false
				}
			}
		case "tools/list":
			if tools, ok := result["tools"].([]any); ok {
				for _, value := range tools {
					if tool, ok := value.(map[string]any); ok {
						tool["execution"] = object{"taskSupport": "forbidden"}
					}
				}
			}
		case "tools/call":
			if _, ok := result["isError"]; !ok {
				result["isError"] = false
			}
		}
	}
	b, err := json.Marshal(body)
	if err != nil || len(b) > mcpMaxBody {
		g.fail(w, core.CapacityExceeded)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(rr.Code)
	_, _ = w.Write(b)
}

// Core lifecycle remains authoritative; close expired or administratively ended
// SDK transports to bound their retained state as well.
func (g *mcpGateway) prune() {
	g.mu.Lock()
	if len(g.bindings) == 0 {
		g.mu.Unlock()
		return
	}
	bindings := make(map[string]*mcpBinding, len(g.bindings))
	for sid, binding := range g.bindings {
		bindings[sid] = binding
	}
	g.mu.Unlock()
	alive := g.owner.Core.LiveClientIDs()
	removed := map[string]bool{}
	g.mu.Lock()
	for sid, binding := range bindings {
		if !alive[binding.clientID] && g.bindings[sid] == binding {
			delete(g.bindings, sid)
			removed[sid] = true
		}
	}
	g.mu.Unlock()
	for session := range g.server.Sessions() {
		if removed[session.ID()] {
			_ = session.Close()
		}
	}
}

func (g *mcpGateway) close() {
	for session := range g.server.Sessions() {
		_ = session.Close()
	}
}
