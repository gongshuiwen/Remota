package contracttest

import (
	"net/http"
	"strings"
	"testing"
)

func TestHTTPContractDetectsDrift(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	for name, value := range map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": strings.Repeat("a", 32),
		"X-Remota-Epoch": strings.Repeat("b", 32), "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
		headers.Set(name, value)
	}
	for _, tc := range []struct {
		name, method, path, request, body string
		status                            int
		edit                              func(*http.Request, http.Header)
		valid                             bool
	}{
		{name: "health", method: "GET", path: "/healthz", status: 200, body: `{"status":"ready","version":"test"}`, valid: true},
		{name: "health_degraded", method: "GET", path: "/healthz", status: 503, body: `{"status":"degraded","version":"test"}`, valid: true},
		{name: "wrong_health_error_model", method: "GET", path: "/healthz", status: 503, body: `{"error":{"code":"STATE_UNAVAILABLE","message":"error","trace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`},
		{name: "undocumented_status", method: "GET", path: "/healthz", status: 202, body: `{}`},
		{name: "undocumented_route", method: "GET", path: "/undocumented", status: 200, body: `{}`},
		{name: "wrong_body", method: "GET", path: "/healthz", status: 200, body: `{"status":"ready"}`},
		{name: "wrong_media", method: "GET", path: "/healthz", status: 200, body: `{"status":"ready","version":"test"}`,
			edit: func(_ *http.Request, h http.Header) { h.Set("Content-Type", "text/plain") }},
		{name: "missing_epoch", method: "GET", path: "/healthz", status: 200, body: `{"status":"ready","version":"test"}`,
			edit: func(_ *http.Request, h http.Header) { h.Del("X-Remota-Epoch") }},
		{name: "invalid_epoch", method: "GET", path: "/healthz", status: 200, body: `{"status":"ready","version":"test"}`,
			edit: func(_ *http.Request, h http.Header) { h.Set("X-Remota-Epoch", "invalid") }},
		{name: "204_has_body", method: "POST", path: "/api/v1/admin/logout", request: `{}`, status: 204, body: `{}`,
			edit: func(_ *http.Request, h http.Header) { h.Set("Set-Cookie", "remota_admin=; Path=/") }},
		{name: "accepted_logout", method: "POST", path: "/api/v1/admin/logout", request: `{}`, status: 204, valid: true},
		{name: "accepted_wrong_request_model", method: "POST", path: "/api/v1/admin/logout", request: `{"unknown":true}`, status: 204},
		{name: "accepted_missing_request_body", method: "POST", path: "/api/v1/admin/logout", status: 204},
		{name: "rejected_invalid_request", method: "POST", path: "/api/v1/admin/logout", request: `{"unknown":true}`, status: 400,
			body: `{"error":{"code":"UNKNOWN_FIELD","message":"error","trace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, "http://127.0.0.1"+tc.path, strings.NewReader(tc.request))
			if err != nil {
				t.Fatal(err)
			}
			h := headers.Clone()
			if tc.path == "/api/v1/admin/logout" {
				req.Header.Set("Content-Type", "application/json")
				h.Set("Set-Cookie", "remota_admin=; Path=/")
			}
			if tc.edit != nil {
				tc.edit(req, h)
			}
			if err := contract.Check(req, tc.status, h, []byte(tc.body)); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, contract error=%v", tc.valid, err)
			}
		})
	}
}
