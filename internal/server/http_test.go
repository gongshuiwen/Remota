package server

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"remota.local/remota/internal/auth"
	"remota.local/remota/internal/clock"
	"remota.local/remota/internal/protocol"
	"remota.local/remota/internal/server/core"
	"remota.local/remota/internal/server/store"
)

type fakeClock struct {
	mu sync.Mutex
	n  clock.Sample
}

func (f *fakeClock) now() clock.Sample { f.mu.Lock(); defer f.mu.Unlock(); return f.n }
func (f *fakeClock) step(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n.Wall = f.n.Wall.Add(d)
	f.n.Elapsed += d
}

func (f *fakeClock) advance(wall, elapsed time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n.Wall = f.n.Wall.Add(wall)
	f.n.Elapsed += elapsed
}

type webHarness struct {
	s       *Server
	web     *httptest.Server
	f       *fakeClock
	dir     string
	handoff store.Handoff
	cookie  *http.Cookie
	csrf    string
}
type response struct {
	status int
	header http.Header
	body   []byte
	value  object
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newWeb(t *testing.T, configure func(*Server)) *webHarness {
	t.Helper()
	h := &webHarness{dir: t.TempDir(), f: &fakeClock{n: clock.Sample{Wall: time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC)}}}
	_, handoff, err := store.Initialize(h.dir, h.f.now().Wall)
	must(t, err)
	h.handoff = handoff
	h.web = httptest.NewUnstartedServer(nil)
	h.s, err = Open(Config{StateDir: h.dir, Listen: h.web.Listener.Addr().String(), Clock: h.f.now})
	must(t, err)
	if configure != nil {
		configure(h.s)
	}
	h.web.Config.Handler = h.s.Handler()
	h.web.Start()
	h.web.Client().Timeout = 3 * time.Second
	t.Cleanup(func() { h.web.Close(); must(t, h.s.Close()) })
	h.login(t)
	return h
}

func (h *webHarness) do(t *testing.T, method, path, body string, edit func(*http.Request)) response {
	t.Helper()
	req, err := http.NewRequest(method, h.web.URL+path, strings.NewReader(body))
	must(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.web.URL)
	if h.s.config.PublicURL != "" {
		u, err := url.Parse(h.s.config.PublicURL)
		must(t, err)
		req.Host = u.Host
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}
	if h.cookie != nil {
		req.AddCookie(h.cookie)
	}
	if h.csrf != "" {
		req.Header.Set("X-CSRF-Token", h.csrf)
	}
	if edit != nil {
		edit(req)
	}
	r, err := h.web.Client().Do(req)
	must(t, err)
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	must(t, err)
	v := object{}
	if len(b) > 0 {
		must(t, json.Unmarshal(b, &v))
	}
	if r.Header.Get("X-Remota-Epoch") != h.s.Core.Epoch() || len(r.Header.Get("X-Request-ID")) != 32 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("missing response identity/cache headers")
	}
	if r.StatusCode >= 400 && path != "/healthz" {
		parsed, err := protocol.Parse(b)
		must(t, err)
		must(t, h.s.validator.Validate("Error", parsed))
	}
	return response{r.StatusCode, r.Header, b, v}
}
func (h *webHarness) login(t *testing.T) {
	t.Helper()
	b, err := json.Marshal(object{"password": h.handoff.AdminPassword})
	must(t, err)
	r := h.do(t, "POST", "/api/v1/admin/login", string(b), nil)
	check(t, h, r, 200, "AdminLoginResponse")
	cookies := (&http.Response{Header: r.header}).Cookies()
	if len(cookies) != 1 {
		t.Fatal("login cookie missing")
	}
	h.cookie = cookies[0]
	h.csrf = r.value["csrf_token"].(string)
}
func check(t *testing.T, h *webHarness, r response, status int, model string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status=%d want=%d body=%s", r.status, status, r.body)
	}
	if model != "" {
		v, err := protocol.Parse(r.body)
		must(t, err)
		must(t, h.s.validator.Validate(model, v))
	}
}
func wantError(t *testing.T, r response, status int, code string) {
	t.Helper()
	if r.status != status || r.value["error"].(map[string]any)["code"] != code {
		t.Fatalf("want %d/%s got %d %s", status, code, r.status, r.body)
	}
}
func createBody(op, kind string) string {
	return fmt.Sprintf(`{"operation_id":%q,"kind":%q,"name":" helper ","expires_in_seconds":60.0}`, op, kind)
}
func (h *webHarness) state(t *testing.T) response {
	t.Helper()
	r := h.do(t, "GET", "/api/v1/admin/state", "", nil)
	check(t, h, r, 200, "AdminSnapshot")
	return r
}

func TestAdminCredentialLifecycle(t *testing.T) {
	h := newWeb(t, nil)
	if !h.cookie.HttpOnly || h.cookie.SameSite != http.SameSiteStrictMode || h.cookie.Path != "/" || h.cookie.Domain != "" || h.cookie.Secure {
		t.Fatal("cookie policy differs")
	}
	for _, item := range []struct{ secret, kind string }{{h.handoff.Access, "access"}, {h.handoff.MCP, "mcp"}} {
		_, err := h.s.Core.AuthenticateCredential(item.secret, item.kind)
		must(t, err)
		other := "mcp"
		if item.kind == "mcp" {
			other = "access"
		}
		if _, err = h.s.Core.AuthenticateCredential(item.secret, other); err != core.Unauthenticated {
			t.Fatal("credential role confusion")
		}
	}
	state := h.state(t)
	if state.value["summary"].(map[string]any)["active_access_credentials"] != float64(1) {
		t.Fatal("initial summary differs")
	}
	op := strings.Repeat("a", 32)
	r := h.do(t, "POST", "/api/v1/admin/credentials", createBody(op, "mcp"), nil)
	check(t, h, r, 201, "CredentialIssue")
	v := r.value["credential"].(map[string]any)
	id := v["id"].(string)
	secret := r.value["secret"].(string)
	if v["name"] != "helper" || v["allow_exec"] != false || r.value["management_revision"] != float64(2) || !auth.ValidSecret(secret, "mcp") {
		t.Fatal("credential defaults or normalization differ")
	}
	_, err := h.s.Core.AuthenticateCredential(secret, "mcp")
	must(t, err)
	r = h.do(t, "POST", "/api/v1/admin/credentials", createBody(op, "access"), nil)
	wantError(t, r, 409, "CREDENTIAL_OPERATION_EXISTS")
	if r.value["error"].(map[string]any)["details"].(map[string]any)["resource_id"] != id {
		t.Fatal("operation result cannot be reconciled")
	}
	invalidName := fmt.Sprintf(`{"operation_id":%q,"kind":"mcp","name":%q}`, op, strings.Repeat("界", 27))
	r = h.do(t, "POST", "/api/v1/admin/credentials", invalidName, nil)
	wantError(t, r, 400, "INVALID_ARGUMENT")
	rotate := fmt.Sprintf(`{"operation_id":%q}`, strings.Repeat("b", 32))
	r = h.do(t, "POST", "/api/v1/admin/credentials/"+id+"/rotate", rotate, nil)
	check(t, h, r, 201, "CredentialIssue")
	newID := r.value["credential"].(map[string]any)["id"].(string)
	newSecret := r.value["secret"].(string)
	if _, err = h.s.Core.AuthenticateCredential(secret, "mcp"); err != core.Unauthenticated {
		t.Fatal("rotation left source credential active")
	}
	_, err = h.s.Core.AuthenticateCredential(newSecret, "mcp")
	must(t, err)
	r = h.do(t, "POST", "/api/v1/admin/credentials/"+id+"/revoke", "{}", nil)
	check(t, h, r, 200, "CredentialRevokeResponse")
	if r.value["management_revision"] != float64(3) {
		t.Fatal("repeated revocation changed durable revision")
	}
	r = h.do(t, "POST", "/api/v1/admin/credentials/"+newID+"/revoke", "{}", nil)
	check(t, h, r, 200, "CredentialRevokeResponse")
	r = h.do(t, "POST", "/api/v1/admin/credentials/"+id+"/rotate", rotate, nil)
	wantError(t, r, 409, "CREDENTIAL_OPERATION_EXISTS")
	r = h.do(t, "POST", "/api/v1/admin/credentials/"+id+"/rotate", fmt.Sprintf(`{"operation_id":%q}`, strings.Repeat("c", 32)), nil)
	wantError(t, r, 409, "CREDENTIAL_REVOKED")
	h.f.step(60 * time.Second)
	if _, err = h.s.Core.AuthenticateCredential(newSecret, "mcp"); err != core.Unauthenticated {
		t.Fatal("revoked credential accepted")
	}
	state = h.state(t)
	for _, x := range []string{secret, newSecret, h.handoff.Access, h.handoff.MCP, h.handoff.AdminPassword, h.cookie.Value} {
		if strings.Contains(string(state.body), x) {
			t.Fatal("secret leaked in snapshot")
		}
	}
	r = h.do(t, "POST", "/api/v1/admin/logout", "{}", nil)
	check(t, h, r, 204, "")
	r = h.do(t, "GET", "/api/v1/admin/state", "", nil)
	wantError(t, r, 401, "UNAUTHENTICATED")
}

func TestCredentialLifetimeBoundariesSurviveRestart(t *testing.T) {
	h := newWeb(t, nil)
	var secrets []string
	for i, tc := range []struct {
		seconds int64
		want    time.Duration
	}{
		{0, 90 * 24 * time.Hour},
		{60, time.Minute},
		{31536000, 365 * 24 * time.Hour},
		{59, 0},
		{31536001, 0},
	} {
		expiry := ""
		if tc.seconds != 0 {
			expiry = fmt.Sprintf(`,"expires_in_seconds":%d`, tc.seconds)
		}
		body := fmt.Sprintf(`{"operation_id":"%032x","kind":"mcp","name":"lifetime"%s}`, i+1, expiry)
		r := h.do(t, "POST", "/api/v1/admin/credentials", body, nil)
		if tc.want == 0 {
			wantError(t, r, 400, "INVALID_ARGUMENT")
			continue
		}
		check(t, h, r, 201, "CredentialIssue")
		credential := r.value["credential"].(map[string]any)
		created, err := time.Parse(time.RFC3339Nano, credential["created_at"].(string))
		must(t, err)
		expires, err := time.Parse(time.RFC3339Nano, credential["expires_at"].(string))
		must(t, err)
		if expires.Sub(created) != tc.want {
			t.Fatalf("credential lifetime differs: %v", expires.Sub(created))
		}
		secrets = append(secrets, r.value["secret"].(string))
	}
	h.web.Close()
	must(t, h.s.Close())
	next, err := Open(h.s.config)
	must(t, err)
	defer next.Close()
	for _, secret := range secrets {
		_, err := next.Core.AuthenticateCredential(secret, "mcp")
		must(t, err)
	}
}

func TestHTTPIdentityAndStrictMessages(t *testing.T) {
	h := newWeb(t, nil)
	body := createBody(strings.Repeat("d", 32), "access")
	for _, tc := range []struct {
		name, method, path, body, code string
		status                         int
		edit                           func(*http.Request)
	}{
		{"cookie_required", "GET", "/api/v1/admin/state", "", "UNAUTHENTICATED", 401, func(r *http.Request) { r.Header.Del("Cookie") }},
		{"bearer_denied", "GET", "/api/v1/admin/state", "", "UNAUTHENTICATED", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+h.handoff.MCP) }},
		{"duplicate_cookie", "GET", "/api/v1/admin/state", "", "UNAUTHENTICATED", 401, func(r *http.Request) { r.AddCookie(h.cookie) }},
		{"empty_duplicate_cookie", "GET", "/api/v1/admin/state", "", "UNAUTHENTICATED", 401, func(r *http.Request) { r.Header.Set("Cookie", "remota_admin=; remota_admin="+h.cookie.Value) }},
		{"host", "GET", "/api/v1/admin/state", "", "HOST_DENIED", 403, func(r *http.Request) {
			r.Host = "attacker.invalid"
			r.Header.Set("X-Forwarded-Host", strings.TrimPrefix(h.web.URL, "http://"))
		}},
		{"origin", "POST", "/api/v1/admin/credentials", body, "ORIGIN_DENIED", 403, func(r *http.Request) { r.Header.Set("Origin", "null") }},
		{"origin_empty_fragment", "POST", "/api/v1/admin/credentials", body, "ORIGIN_DENIED", 403, func(r *http.Request) { r.Header.Set("Origin", h.web.URL+"#") }},
		{"missing_origin", "POST", "/api/v1/admin/credentials", body, "ORIGIN_DENIED", 403, func(r *http.Request) { r.Header.Del("Origin") }},
		{"csrf", "POST", "/api/v1/admin/credentials", body, "CSRF_INVALID", 403, func(r *http.Request) { r.Header.Set("X-CSRF-Token", "wrong") }},
		{"query", "GET", "/api/v1/admin/state?unexpected=1", "", "INVALID_ARGUMENT", 400, nil},
		{"duplicate_json", "POST", "/api/v1/admin/logout", `{"a":1,"a":2}`, "INVALID_JSON", 400, nil},
		{"trailing_json", "POST", "/api/v1/admin/logout", `{} {}`, "INVALID_JSON", 400, nil},
		{"surrogate", "POST", "/api/v1/admin/logout", `{"a":"\ud800"}`, "INVALID_JSON", 400, nil},
		{"utf8", "POST", "/api/v1/admin/logout", string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}), "INVALID_JSON", 400, nil},
		{"unknown_field", "POST", "/api/v1/admin/logout", `{"unexpected":true}`, "UNKNOWN_FIELD", 400, nil},
		{"empty_password", "POST", "/api/v1/admin/login", `{"password":""}`, "UNAUTHENTICATED", 401, nil},
		{"empty_password_unknown_field", "POST", "/api/v1/admin/login", `{"password":"","extra":true}`, "UNKNOWN_FIELD", 400, nil},
		{"password_bytes", "POST", "/api/v1/admin/login", `{"password":"` + strings.Repeat("界", 86) + `"}`, "UNAUTHENTICATED", 401, nil},
		{"access_exec", "POST", "/api/v1/admin/credentials", strings.TrimSuffix(body, "}") + `,"allow_exec":false}`, "INVALID_ARGUMENT", 400, nil},
		{"content_type", "POST", "/api/v1/admin/logout", `{}`, "UNSUPPORTED_MEDIA_TYPE", 415, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"compressed", "POST", "/api/v1/admin/logout", `{}`, "UNSUPPORTED_MEDIA_TYPE", 415, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
		{"body_limit", "POST", "/api/v1/admin/logout", strings.Repeat(" ", 256<<10) + "{}", "PAYLOAD_TOO_LARGE", 413, nil},
		{"get_body", "GET", "/api/v1/admin/state", `{}`, "INVALID_ARGUMENT", 400, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(t, tc.method, tc.path, tc.body, tc.edit)
			wantError(t, r, tc.status, tc.code)
		})
	}
	r := h.do(t, "PUT", "/api/v1/admin/logout", "{}", nil)
	check(t, h, r, 405, "Error")
	if r.header.Get("Allow") != "POST" {
		t.Fatal("missing Allow")
	}
	if len(h.state(t).value["credentials"].([]any)) != 2 {
		t.Fatal("invalid requests changed credentials")
	}
}

func deviceTask(t *testing.T, h *webHarness) (string, string, core.Task, core.Grant) {
	t.Helper()
	access, err := h.s.Core.AuthenticateCredential(h.handoff.Access, "access")
	must(t, err)
	mcp, err := h.s.Core.AuthenticateCredential(h.handoff.MCP, "mcp")
	must(t, err)
	cl, err := h.s.Core.BeginClient(mcp, core.ClientInfo{Name: "integration", Version: "1"})
	must(t, err)
	_, err = h.s.Core.ClientMessage(mcp, cl.ID, true)
	must(t, err)
	s, _, err := h.s.Core.Register(access)
	must(t, err)
	_, err = h.s.Core.Decide(s.ID, "accept", "full")
	must(t, err)
	p, err := h.s.Core.BeginPoll(s.ID, 1, nil)
	must(t, err)
	_, err = h.s.Core.CompletePoll(p)
	must(t, err)
	task, _, err := h.s.Core.Submit(mcp, cl.ID, s.ID, strings.Repeat("1", 32), core.Spec{Command: "private command", Reason: "private reason", Shell: "sh", TimeoutSeconds: 120})
	must(t, err)
	_, g, err := h.s.Core.Start(s.ID, task.ID, 1)
	must(t, err)
	_, err = h.s.Core.RecordReceipt(s.ID, task.ID, core.Receipt{Kind: "started", GrantID: g.ID})
	must(t, err)
	return access, cl.ID, task, g
}

func TestNotesEndsAndConsistentSnapshot(t *testing.T) {
	h := newWeb(t, nil)
	_, cl, task, g := deviceTask(t, h)
	state := h.state(t)
	summary := state.value["summary"].(map[string]any)
	if summary["live_sessions"] != float64(1) || summary["ready_clients"] != float64(1) || strings.Contains(string(state.body), "private command") {
		t.Fatal("snapshot counts or disclosure differ")
	}
	for _, item := range state.value["credentials"].([]any) {
		credential := item.(map[string]any)
		counts := credential["effects_count"].(map[string]any)
		if counts["unsettled_tasks"] != float64(1) {
			t.Fatal("task not counted for both credentials")
		}
		switch credential["kind"] {
		case "access":
			if counts["live_sessions"] != float64(1) || counts["live_clients"] != float64(0) {
				t.Fatal("access credential counts differ")
			}
		case "mcp":
			if counts["live_sessions"] != float64(0) || counts["live_clients"] != float64(1) {
				t.Fatal("MCP credential counts differ")
			}
		default:
			t.Fatal("unexpected credential kind")
		}
	}
	path := "/api/v1/admin/sessions/" + task.SessionID
	r := h.do(t, "PATCH", path, `{"note":"备注","expected_note_revision":0}`, nil)
	check(t, h, r, 200, "AdminSessionResponse")
	r = h.do(t, "PATCH", path, `{"note":"旧请求","expected_note_revision":0}`, nil)
	wantError(t, r, 409, "REVISION_CONFLICT")
	r = h.do(t, "POST", "/api/v1/admin/clients/"+cl+"/end", "{}", nil)
	check(t, h, r, 200, "MCPClientEndResponse")
	if r.value["effects"].(map[string]any)["tasks_stop_requested"] != float64(1) {
		t.Fatal("stop effect count differs")
	}
	r = h.do(t, "POST", path+"/end", "{}", nil)
	check(t, h, r, 200, "SessionResponse")
	r = h.do(t, "PATCH", path, `{"note":"no","expected_note_revision":1}`, nil)
	wantError(t, r, 410, "SESSION_ENDED")
	h.f.step(5 * time.Second)
	state = h.state(t)
	session := state.value["sessions"].([]any)[0].(map[string]any)
	if session["note"] != "" || session["cleanup"] != "unknown" {
		t.Fatal("ended data or exit evidence incorrect")
	}
	_, err := h.s.Core.RecordReceipt(task.SessionID, task.ID, core.Receipt{Kind: "exited", GrantID: g.ID})
	must(t, err)
	r = h.do(t, "POST", path+"/end", "{}", nil)
	check(t, h, r, 200, "SessionResponse")
	if r.value["session"].(map[string]any)["cleanup"] != "confirmed" {
		t.Fatal("late exit did not update cleanup")
	}
}

func TestCredentialRevocationEffects(t *testing.T) {
	for _, tc := range []struct {
		name            string
		kind            string
		alreadyStopping bool
		sessions        float64
		clients         float64
		tasks           float64
	}{
		{"access", "access", false, 1, 0, 1},
		{"mcp", "mcp", false, 0, 1, 1},
		{"already_stopping", "mcp", true, 0, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWeb(t, nil)
			access, clientID, task, _ := deviceTask(t, h)
			if tc.alreadyStopping {
				_, err := h.s.Core.Cancel(task.CredentialID, clientID, task.SessionID, task.ID)
				must(t, err)
			}
			id := task.CredentialID
			if tc.kind == "access" {
				id = access
			}
			path := "/api/v1/admin/credentials/" + id + "/revoke"
			r := h.do(t, "POST", path, "{}", nil)
			check(t, h, r, 200, "CredentialRevokeResponse")
			effects := r.value["effects"].(map[string]any)
			if effects["sessions_ended"] != tc.sessions || effects["clients_ended"] != tc.clients || effects["tasks_stop_requested"] != tc.tasks {
				t.Fatalf("unexpected effects: %v", effects)
			}
			r = h.do(t, "POST", path, "{}", nil)
			check(t, h, r, 200, "CredentialRevokeResponse")
			effects = r.value["effects"].(map[string]any)
			if effects["sessions_ended"] != float64(0) || effects["clients_ended"] != float64(0) || effects["tasks_stop_requested"] != float64(0) {
				t.Fatalf("repeated revocation reported new effects: %v", effects)
			}
		})
	}
}

func TestHTTPSAuthorityAndCookie(t *testing.T) {
	h := newWeb(t, func(s *Server) {
		config := s.config
		config.PublicURL = "https://Example.COM/"
		a, o, m, err := config.validate()
		must(t, err)
		if a != "example.com:443" || o != "https://example.com:443" || m != "https" {
			t.Fatal("authority normalization differs")
		}
		s.authority, s.origin, s.publicMode, s.secure, s.config.PublicURL = a, o, m, true, config.PublicURL
	})
	// A real backend HTTP request models an external TLS-terminating proxy.
	r := h.do(t, "POST", "/api/v1/admin/login", fmt.Sprintf(`{"password":%q}`, h.handoff.AdminPassword), func(r *http.Request) { r.Host = "EXAMPLE.com:0443"; r.Header.Set("Origin", "https://example.com") })
	check(t, h, r, 200, "AdminLoginResponse")
	c := (&http.Response{Header: r.header}).Cookies()[0]
	if !c.Secure || !c.HttpOnly {
		t.Fatal("HTTPS cookie not protected")
	}
	config := h.s.config
	for _, address := range []string{"https://user:password@example.com", "https://example.com/sub", "https://example.com?q=1", "https://example.com#", "http://example.com"} {
		config.PublicURL = address
		config.AllowInsecureHTTP = false
		if _, _, _, err := config.validate(); err == nil {
			t.Fatalf("invalid root accepted: %s", address)
		}
	}
}

func TestInternationalizedPublicURL(t *testing.T) {
	h := newWeb(t, func(s *Server) {
		config := s.config
		config.PublicURL = "https://bücher.example/"
		a, o, m, err := config.validate()
		must(t, err)
		if a != "xn--bcher-kva.example:443" || o != "https://xn--bcher-kva.example:443" || m != "https" {
			t.Fatal("internationalized authority normalization differs")
		}
		s.authority, s.origin, s.publicMode, s.secure, s.config.PublicURL = a, o, m, true, config.PublicURL
	})
	r := h.do(t, "POST", "/api/v1/admin/login", fmt.Sprintf(`{"password":%q}`, h.handoff.AdminPassword), func(r *http.Request) {
		r.Host = "xn--bcher-kva.example"
		r.Header.Set("Origin", "https://xn--bcher-kva.example")
	})
	check(t, h, r, 200, "AdminLoginResponse")
	config := h.s.config
	config.PublicURL = "https://x\u200dy.example"
	if _, _, _, err := config.validate(); err == nil {
		t.Fatal("invalid internationalized domain accepted")
	}
}

func TestLoginRateReplacementAndAbsoluteExpiry(t *testing.T) {
	h := newWeb(t, nil)
	other := *h
	other.cookie = nil
	other.csrf = ""
	other.login(t)
	old := h.cookie.Value
	h.login(t)
	r := h.do(t, "GET", "/api/v1/admin/state", "", func(r *http.Request) { r.Header.Set("Cookie", "remota_admin="+old) })
	wantError(t, r, 401, "UNAUTHENTICATED")
	other.state(t)
	for i := 0; i < 5; i++ {
		r = h.do(t, "POST", "/api/v1/admin/login", `{"password":"wrong"}`, nil)
		wantError(t, r, 401, "UNAUTHENTICATED")
		if i < 4 {
			h.f.step(time.Second)
		}
	}
	r = h.do(t, "POST", "/api/v1/admin/login", `{"password":"wrong"}`, func(r *http.Request) { r.Header.Set("X-Forwarded-For", "192.0.2.1") })
	wantError(t, r, 429, "RATE_LIMITED")
	if r.header.Get("Retry-After") != "56" {
		t.Fatalf("retry budget differs: %q", r.header.Get("Retry-After"))
	}
	h.f.step(time.Minute)
	h.state(t)
	h.f.step(8*time.Hour - time.Minute - 4*time.Second)
	r = h.do(t, "GET", "/api/v1/admin/state", "", nil)
	wantError(t, r, 401, "UNAUTHENTICATED")
}

func TestAdminSessionLimitAndReplacement(t *testing.T) {
	h := newWeb(t, nil)
	var other *http.Cookie
	for i := 0; i < 7; i++ {
		r := h.do(t, "POST", "/api/v1/admin/login", fmt.Sprintf(`{"password":%q}`, h.handoff.AdminPassword), func(r *http.Request) {
			r.Header.Del("Cookie")
		})
		check(t, h, r, 200, "AdminLoginResponse")
		if i == 0 {
			other = (&http.Response{Header: r.header}).Cookies()[0]
		}
	}
	r := h.do(t, "POST", "/api/v1/admin/login", fmt.Sprintf(`{"password":%q}`, h.handoff.AdminPassword), func(r *http.Request) {
		r.Header.Del("Cookie")
	})
	wantError(t, r, 503, "CAPACITY_EXCEEDED")
	h.login(t)
	h.state(t)
	r = h.do(t, "GET", "/api/v1/admin/state", "", func(r *http.Request) {
		r.Header.Set("Cookie", other.String())
	})
	check(t, h, r, 200, "AdminSnapshot")
}

func TestSlowCommitDoesNotBlockStopOrSnapshot(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h := newWeb(t, func(s *Server) {
		actual := s.persist
		s.persist = func(ctx context.Context, c store.Change) store.Outcome {
			close(started)
			<-release
			return actual(ctx, c)
		}
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	access, _, task, _ := deviceTask(t, h)
	done := make(chan response, 1)
	go func() {
		defer close(done)
		done <- h.do(t, "POST", "/api/v1/admin/credentials/"+access+"/revoke", "{}", nil)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("commit not reached")
	}
	h.f.step(5 * time.Second)
	state := h.state(t)
	if state.value["server"].(map[string]any)["management_revision"] != float64(1) {
		t.Fatal("uncommitted write reported durable")
	}
	if state.value["sessions"].([]any)[0].(map[string]any)["cleanup"] != "unknown" {
		t.Fatal("slow commit blocked stop deadline")
	}
	if _, err := h.s.Core.AuthenticateCredential(h.handoff.Access, "access"); err != core.Unauthenticated {
		t.Fatal("old identity still grants authority")
	}
	session, err := h.s.Core.GetSession(task.SessionID)
	must(t, err)
	if session.EndReason != "access_invalid" {
		t.Fatal("revocation cause changed")
	}
	once.Do(func() { close(release) })
	r := <-done
	check(t, h, r, 200, "CredentialRevokeResponse")
}

func TestWriteFailureAndUnknownCommitRequireRecovery(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			h := newWeb(t, func(s *Server) {
				actual := s.persist
				s.persist = func(ctx context.Context, c store.Change) store.Outcome {
					if unknown {
						r := actual(ctx, c)
						if r != store.Committed {
							return r
						}
						return store.Unknown
					}
					return store.NotCommitted
				}
			})
			op := strings.Repeat("e", 32)
			r := h.do(t, "POST", "/api/v1/admin/credentials", createBody(op, "access"), nil)
			wantError(t, r, 503, "STATE_UNAVAILABLE")
			state := h.state(t)
			if state.value["server"].(map[string]any)["health"] != "degraded" || state.value["server"].(map[string]any)["management_revision"] != float64(1) {
				t.Fatal("failed write reported success")
			}
			r = h.do(t, "GET", "/healthz", "", nil)
			check(t, h, r, 503, "HealthResponse")
			epoch := h.s.Core.Epoch()
			h.web.Close()
			must(t, h.s.Close())
			_, err := Open(h.s.config)
			if !errors.Is(err, store.ErrRecovery) {
				t.Fatalf("uncertain state resumed: %v", err)
			}
			d, _, err := store.Recover(h.dir, h.f.now().Wall, nil, false)
			must(t, err)
			if unknown {
				if len(d.Credentials) != 3 {
					t.Fatal("committed operation not found")
				}
				for _, record := range d.Credentials {
					if record.Credential.CreationOperationID == op && !record.Credential.Revoked {
						t.Fatal("unknown handoff remained active")
					}
				}
			} else if len(d.Credentials) != 2 {
				t.Fatal("failed operation persisted")
			}
			next, err := Open(h.s.config)
			must(t, err)
			defer next.Close()
			if next.Core.Epoch() == epoch || len(next.Core.Snapshot().Sessions) != 0 {
				t.Fatal("restart reused execution context")
			}
		})
	}
}

func TestFailedRevocationTightensMemoryUntilRecovery(t *testing.T) {
	h := newWeb(t, func(s *Server) {
		s.persist = func(context.Context, store.Change) store.Outcome {
			return store.NotCommitted
		}
	})
	access, _, task, _ := deviceTask(t, h)
	r := h.do(t, "POST", "/api/v1/admin/credentials/"+access+"/revoke", "{}", nil)
	wantError(t, r, 503, "STATE_UNAVAILABLE")
	state := h.state(t)
	if state.value["server"].(map[string]any)["health"] != "degraded" {
		t.Fatal("failed revocation did not degrade service")
	}
	for _, v := range state.value["credentials"].([]any) {
		c := v.(map[string]any)
		if c["id"] == access && c["status"] != "active" {
			t.Fatal("uncommitted revocation reported as durable")
		}
	}
	if _, err := h.s.Core.AuthenticateCredential(h.handoff.Access, "access"); err != core.Unauthenticated {
		t.Fatal("failed revocation restored in-memory access")
	}
	h.f.step(5 * time.Second)
	session, err := h.s.Core.GetSession(task.SessionID)
	must(t, err)
	if session.EndReason != "access_invalid" || session.Cleanup != "unknown" {
		t.Fatal("failed write blocked cleanup")
	}
	h.web.Close()
	must(t, h.s.Close())
	data, _, err := store.Recover(h.dir, h.f.now().Wall, nil, false)
	must(t, err)
	for _, r := range data.Credentials {
		if r.Credential.ID == access && !r.Credential.Revoked {
			t.Fatal("recovery did not persist the required revocation")
		}
	}
	next, err := Open(h.s.config)
	must(t, err)
	defer next.Close()
	if _, err = next.Core.AuthenticateCredential(h.handoff.Access, "access"); err != core.Unauthenticated {
		t.Fatal("recovered access revived")
	}
	if _, err = next.Core.AuthenticateCredential(h.handoff.MCP, "mcp"); err != nil {
		t.Fatal("unrelated credential changed")
	}
}

func TestExpiredOrReplacedLoginCannotReceiveIssuance(t *testing.T) {
	for _, mode := range []string{"expiry", "replace"} {
		t.Run(mode, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			h := newWeb(t, func(s *Server) {
				actual := s.persist
				s.persist = func(ctx context.Context, c store.Change) store.Outcome {
					close(started)
					<-release
					return actual(ctx, c)
				}
			})
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			done := make(chan response, 1)
			old := *h
			go func() {
				defer close(done)
				done <- old.do(t, "POST", "/api/v1/admin/credentials", createBody(strings.Repeat("f", 32), "access"), nil)
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("commit not reached")
			}
			if mode == "expiry" {
				h.f.step(8 * time.Hour)
			} else {
				h.login(t)
			}
			once.Do(func() { close(release) })
			r := <-done
			wantError(t, r, 401, "UNAUTHENTICATED")
			if mode == "expiry" {
				h.login(t)
			}
			state := h.state(t)
			if len(state.value["credentials"].([]any)) != 3 || state.value["server"].(map[string]any)["management_revision"] != float64(2) {
				t.Fatal("confirmed commit lost after response identity expired")
			}
		})
	}
}

func TestGlobalLoginFailureLimit(t *testing.T) {
	h := newWeb(t, nil)
	request := func(ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", h.web.URL+"/api/v1/admin/login", strings.NewReader(`{"password":"wrong"}`))
		req.RemoteAddr = ip + ":1234"
		req.Header.Set("Origin", h.web.URL)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.s.Handler().ServeHTTP(w, req)
		return w
	}
	for i := 0; i < 30; i++ {
		if w := request(fmt.Sprintf("192.0.2.%d", i)); w.Code != 401 {
			t.Fatalf("unexpected failure %d", w.Code)
		}
	}
	if w := request("198.51.100.1"); w.Code != 429 {
		t.Fatal("global login failure budget bypassed")
	}
}

func TestSnapshotUsesElapsedCredentialExpiry(t *testing.T) {
	h := newWeb(t, nil)
	body := fmt.Sprintf(`{"operation_id":%q,"kind":"mcp","name":"short","expires_in_seconds":604830}`, strings.Repeat("a", 32))
	r := h.do(t, "POST", "/api/v1/admin/credentials", body, nil)
	check(t, h, r, 201, "CredentialIssue")
	secret := r.value["secret"].(string)
	h.f.advance(-48*time.Hour, time.Minute)
	v := h.state(t)
	if v.value["summary"].(map[string]any)["expiring_credentials"] != float64(1) {
		t.Fatal("wall rollback hid the elapsed credential expiry")
	}
	h.f.advance(0, 604770*time.Second)
	if _, err := h.s.Core.AuthenticateCredential(secret, "mcp"); err != core.Unauthenticated {
		t.Fatal("credential survived elapsed expiry")
	}
}

func TestPublicURLLoopbackExceptionIsLiteral(t *testing.T) {
	if _, err := normalizeAuthority("example.com?", "https"); err == nil {
		t.Fatal("authority with query delimiter accepted")
	}
	if _, err := normalizeAuthority("example.com#", "https"); err == nil {
		t.Fatal("authority with fragment delimiter accepted")
	}
	config := Config{Listen: "127.0.0.1:8080", PublicURL: "http://localhost:8080"}
	if _, _, _, err := config.validate(); err == nil {
		t.Fatal("DNS name received literal loopback exception")
	}
	config.PublicURL = "http://１２７.０.０.１:8080"
	if _, _, _, err := config.validate(); err == nil {
		t.Fatal("nonliteral address received literal loopback exception")
	}
	config.PublicURL = "http://localhost:8080"
	config.AllowInsecureHTTP = true
	_, _, m, err := config.validate()
	must(t, err)
	if m != "insecure_http" {
		t.Fatal("insecure mode not recorded")
	}
}
