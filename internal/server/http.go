package server

import (
	"errors"
	"io"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"
	"remota.local/remota/internal/auth"
	"remota.local/remota/internal/protocol"
	"remota.local/remota/internal/server/core"
	webui "remota.local/remota/web"
)

func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

func (s *Server) respond(w http.ResponseWriter, status int, value any) {
	b, err := json.Marshal(value)
	if err != nil {
		s.fail(w, core.Code("INTERNAL_ERROR"))
		return
	}
	if len(b) > 8<<20 {
		s.fail(w, core.CapacityExceeded)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(b)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	code := err.Error()
	details := object{}
	var f *core.Failure
	if errors.As(err, &f) {
		code = string(f.Code)
		if f.CurrentRevision != nil {
			details["current_revision"] = *f.CurrentRevision
		}
		if f.ResourceID != "" {
			details["resource_id"] = f.ResourceID
		}
	}
	status := http.StatusInternalServerError
	switch code {
	case "INVALID_JSON", "INVALID_ARGUMENT", "UNKNOWN_FIELD", "PROTOCOL_VERSION_REQUIRED", "PROTOCOL_VERSION_UNSUPPORTED":
		status = 400
	case "UNAUTHENTICATED":
		status = 401
	case "HOST_DENIED", "ORIGIN_DENIED", "CSRF_INVALID", "LOCAL_CONSENT_REQUIRED", "LOCAL_RECONSENT_REQUIRED", "EXEC_NOT_ALLOWED":
		status = 403
	case "NOT_FOUND", "TASK_NOT_FOUND", "MCP_SESSION_NOT_FOUND":
		status = 404
	case "CREDENTIAL_REVOKED", "CREDENTIAL_OPERATION_EXISTS", "REVISION_CONFLICT", "INVALID_STATE", "RESULT_CONFLICT", "ALREADY_POLLING", "IDEMPOTENCY_CONFLICT", "TASK_BUSY":
		status = 409
	case "SESSION_ENDED", "GRANT_EXPIRED", "CALL_EXPIRED":
		status = 410
	case "PAYLOAD_TOO_LARGE":
		status = 413
	case "UNSUPPORTED_MEDIA_TYPE":
		status = 415
	case "RATE_LIMITED", "CREDENTIAL_LIMIT", "TASK_LIMIT":
		status = 429
	case "STATE_UNAVAILABLE", "CAPACITY_EXCEEDED", "AGENT_UNAVAILABLE", "SERVER_STOPPING":
		status = 503
	case "DIAGNOSTIC_TIMEOUT":
		status = 504
	default:
		code = "INTERNAL_ERROR"
	}
	if status == 429 && w.Header().Get("Retry-After") == "" {
		w.Header().Set("Retry-After", "60")
	}
	value := object{"code": code, "message": code, "trace_id": w.Header().Get("X-Request-ID")}
	if len(details) > 0 {
		value["details"] = details
	}
	s.respond(w, status, object{"error": value})
}

func route(path string) (action, id, method string) {
	r, params := matchRoute(adminRoutes, path)
	for _, name := range []string{"cid", "sid", "mcl_id"} {
		if value := params[name]; value != "" {
			return r.Action, value, r.Method
		}
	}
	return r.Action, "", r.Method
}

func cookieToken(r *http.Request) (string, error) {
	if len(r.Header.Values("Authorization")) != 0 {
		return "", core.Unauthenticated
	}
	token := ""
	seen := false
	for _, c := range r.Cookies() {
		if c.Name == "remota_admin" {
			if seen {
				return "", core.Unauthenticated
			}
			seen = true
			token = c.Value
		}
	}
	return token, nil
}

func (s *Server) validOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		return false
	}
	u, err := url.Parse(values[0])
	if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil || strings.Contains(values[0], "#") || u.ForceQuery {
		return false
	}
	a, err := normalizeAuthority(u.Host, u.Scheme)
	return err == nil && u.Scheme+"://"+a == s.origin
}

func jsonMedia(r *http.Request) error {
	if len(r.Header.Values("Content-Type")) != 1 {
		return core.Code("UNSUPPORTED_MEDIA_TYPE")
	}
	ct, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || ct != "application/json" || params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8") {
		return core.Code("UNSUPPORTED_MEDIA_TYPE")
	}
	encodings := r.Header.Values("Content-Encoding")
	if len(encodings) > 1 || len(encodings) == 1 && !strings.EqualFold(encodings[0], "identity") {
		return core.Code("UNSUPPORTED_MEDIA_TYPE")
	}
	return nil
}

func (s *Server) body(w http.ResponseWriter, r *http.Request, model string) (map[string]any, error) {
	if err := jsonMedia(r); err != nil {
		return nil, err
	}
	if r.ContentLength > 256<<10 {
		return nil, core.Code("PAYLOAD_TOO_LARGE")
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			return nil, core.Code("PAYLOAD_TOO_LARGE")
		}
		return nil, protocol.InvalidJSON
	}
	value, err := protocol.Parse(b)
	if err != nil {
		return nil, err
	}
	m, err := s.validator.Object(model, value)
	// Password length failures are authentication failures, including empty input.
	if model == "AdminLoginInput" && err == protocol.InvalidArgument {
		if original, ok := value.(map[string]any); ok {
			if password, ok := original["password"].(string); ok && (len(password) == 0 || len(password) > 256) {
				candidate := maps.Clone(original)
				candidate["password"] = "x"
				if s.validator.Validate(model, candidate) == nil {
					return original, nil
				}
			}
		}
	}
	if model == "ConsentReduction" && err == protocol.InvalidArgument {
		if original, ok := value.(map[string]any); ok && original["mode"] == "full" {
			candidate := maps.Clone(original)
			candidate["mode"] = "readonly"
			if s.validator.Validate(model, candidate) == nil {
				return original, nil
			}
		}
	}
	return m, err
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Request-ID", auth.ID(""))
	w.Header().Set("X-Remota-Epoch", s.Core.Epoch())
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	scheme := "http"
	if s.secure {
		scheme = "https"
	}
	a, err := normalizeAuthority(r.Host, scheme)
	if err != nil || a != s.authority {
		s.fail(w, core.Code("HOST_DENIED"))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/agent/") {
		s.serveAgentHTTP(w, r)
		return
	}
	if r.URL.Path == "/mcp" {
		s.mcp.serveHTTP(w, r)
		return
	}
	action, id, method := route(r.URL.Path)
	file := ""
	if action == "" {
		file = consoleFile(r.URL.Path)
		if file != "" {
			method = "GET"
		}
	}
	if (action == "" && file == "") || r.URL.EscapedPath() != r.URL.Path {
		s.fail(w, core.NotFound)
		return
	}
	if file == "" && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		s.fail(w, core.InvalidArgument)
		return
	}
	if r.Method != method && !(file != "" && r.Method == "HEAD") {
		allow := method
		if file != "" {
			allow = "GET, HEAD"
		}
		w.Header().Set("Allow", allow)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(405)
		json.MarshalWrite(w, object{"error": object{"code": "INVALID_ARGUMENT", "message": "method not allowed", "trace_id": w.Header().Get("X-Request-ID")}})
		return
	}
	if method == "GET" && (r.ContentLength > 0 || len(r.TransferEncoding) > 0) {
		s.fail(w, core.InvalidArgument)
		return
	}
	if file != "" {
		http.ServeFileFS(w, r, webui.Files, file)
		return
	}
	if action == "health" {
		status := s.Core.Status()
		code := 200
		if status != "ready" {
			code = 503
		}
		s.respond(w, code, object{"status": status, "version": Version})
		return
	}
	if method != "GET" && !s.validOrigin(r) {
		s.fail(w, core.Code("ORIGIN_DENIED"))
		return
	}
	token, err := cookieToken(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	csrf := r.Header.Get("X-CSRF-Token")
	if len(r.Header.Values("X-CSRF-Token")) > 1 {
		s.fail(w, core.CSRFInvalid)
		return
	}
	if action == "login" {
		s.login(w, r, token)
		return
	}
	if action == "state" {
		v, a, err := s.Core.AdminState(token)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.respond(w, 200, s.snapshot(v, a))
		return
	}
	if err = s.Core.CheckAdmin(token, csrf, true); err != nil {
		s.fail(w, err)
		return
	}
	model := "Empty"
	switch action {
	case "create":
		model = "CredentialCreate"
	case "rotate":
		model = "CredentialRotate"
	case "note":
		model = "NoteInput"
	}
	in, err := s.body(w, r, model)
	if err != nil {
		s.fail(w, err)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	switch action {
	case "logout":
		if err = s.Core.Logout(token, csrf); err != nil {
			s.fail(w, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "remota_admin", Value: "", Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		w.WriteHeader(204)
	case "create", "rotate", "revoke":
		var input core.CredentialInput
		if action != "revoke" {
			input.OperationID = in["operation_id"].(string)
			if x, ok := in["expires_in_seconds"]; ok {
				input.ExpiresInSeconds = protocol.Integer(x)
			}
		}
		if action == "create" {
			input.Kind = in["kind"].(string)
			input.Name = in["name"].(string)
			input.AllowExec, _ = in["allow_exec"].(bool)
		}
		v, secret, effectsValue, revision, err := s.mutateCredential(token, csrf, action, id, input)
		if err != nil {
			s.fail(w, err)
			return
		}
		out := object{"credential": credential(v), "effects": effects(effectsValue), "management_revision": revision}
		status := 200
		if action != "revoke" {
			out["secret"] = secret
			status = 201
		}
		s.respond(w, status, out)
	case "note":
		v, err := s.Core.AdminNote(token, csrf, id, in["note"].(string), protocol.Integer(in["expected_note_revision"]))
		if err != nil {
			s.fail(w, err)
			return
		}
		s.respond(w, 200, object{"session": s.session(v, true)})
	case "end_session":
		v, err := s.Core.AdminEndSession(token, csrf, id)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.respond(w, 200, object{"session": s.session(v, false)})
	case "end_client":
		v, e, err := s.Core.AdminEndClient(token, csrf, id)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.respond(w, 200, object{"client": client(v), "effects": effects(e)})
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, replace string) {
	in, err := s.body(w, r, "AdminLoginInput")
	if err != nil {
		s.fail(w, err)
		return
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	n := s.config.Clock()
	kept := s.failures[:0]
	count := 0
	var globalWait, sourceWait time.Duration
	source, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		source = r.RemoteAddr
	}
	for _, f := range s.failures {
		age := max(n.Wall.Sub(f.at.Wall), n.Elapsed-f.at.Elapsed)
		if age >= time.Minute {
			continue
		}
		kept = append(kept, f)
		left := time.Minute - age
		if globalWait == 0 || left < globalWait {
			globalWait = left
		}
		if f.source == source {
			count++
			if sourceWait == 0 || left < sourceWait {
				sourceWait = left
			}
		}
	}
	s.failures = kept
	if count >= 5 || len(kept) >= 30 {
		var retry time.Duration
		if count >= 5 {
			retry = sourceWait
		}
		if len(kept) >= 30 {
			retry = max(retry, globalWait)
		}
		seconds := (retry + time.Second - 1) / time.Second
		w.Header().Set("Retry-After", strconv.FormatInt(max(1, int64(seconds)), 10))
		s.fail(w, core.Code("RATE_LIMITED"))
		return
	}
	if !s.password.Verify(in["password"].(string)) {
		s.failures = append(s.failures, loginFailure{source, n})
		s.fail(w, core.Unauthenticated)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	a, err := s.Core.Login(replace)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "remota_admin", Value: a.Token, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, Expires: a.ExpiresAt})
	s.respond(w, 200, object{"csrf_token": a.CSRF, "expires_at": timestamp(a.ExpiresAt)})
}
