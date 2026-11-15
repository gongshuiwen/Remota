package server

import (
	"mime"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"remota.local/remota/internal/clock"
	"remota.local/remota/internal/server/core"
)

func TestConsoleStaticRoutes(t *testing.T) {
	f := &fakeClock{n: clock.Sample{Wall: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}}
	c, err := core.New(f.now, 0)
	must(t, err)
	s := &Server{Core: c, authority: "127.0.0.1:8080"}
	before := c.Snapshot()
	request := func(t *testing.T, method, path, body, host string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
		if host != "" {
			r.Host = host
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Header().Get("Set-Cookie") != "" {
			t.Fatal("static requests must not create an administrator identity")
		}
		return w
	}
	index := request(t, "GET", "/", "", "")
	if index.Code != 200 || !strings.HasPrefix(index.Header().Get("Content-Type"), "text/html") {
		t.Fatal("embedded console is missing")
	}
	assets := regexp.MustCompile(`(?:src|href)="(/assets/[^"?]+)"`).FindAllStringSubmatch(index.Body.String(), -1)
	hasScript, hasStyle := false, false
	for _, asset := range assets {
		w := request(t, "GET", asset[1], "", "")
		kind, _, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
		must(t, err)
		want := "javascript"
		hasScript = hasScript || strings.HasSuffix(asset[1], ".js")
		if strings.HasSuffix(asset[1], ".css") {
			want = "text/css"
			hasStyle = true
		}
		if w.Code != 200 || w.Body.Len() == 0 || !strings.Contains(kind, want) || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("built resource is unavailable or has an incorrect content type: %s", asset[1])
		}
	}
	if !hasScript || !hasStyle {
		t.Fatal("console must reference both JavaScript and CSS")
	}
	for _, tc := range []struct {
		method, path, body, host string
		status                   int
		allow                    string
	}{
		{"HEAD", "/", "", "", 200, ""},
		{"GET", "/?view=devices", "", "", 200, ""},
		{"POST", "/", "", "", 405, "GET, HEAD"},
		{"GET", "/", "body", "", 400, ""},
		{"GET", "/", "", "untrusted.example", 403, ""},
		{"GET", "/assets/missing.js", "", "", 404, ""},
		{"GET", "/assets/", "", "", 404, ""},
		{"GET", "/assets/../index.html", "", "", 404, ""},
		{"GET", "/assets/%2e%2e/index.html", "", "", 404, ""},
		{"GET", "/src/main.tsx", "", "", 404, ""},
		{"GET", "/api/v1/admin/missing", "", "", 404, ""},
		{"GET", "/api/v1/admin/state", "", "", 401, ""},
	} {
		t.Run(tc.method+" "+tc.path+" "+tc.host, func(t *testing.T) {
			w := request(t, tc.method, tc.path, tc.body, tc.host)
			if w.Code != tc.status || w.Header().Get("Allow") != tc.allow {
				t.Fatalf("status/Allow = %d/%q, want %d/%q", w.Code, w.Header().Get("Allow"), tc.status, tc.allow)
			}
			if tc.method == "HEAD" && (w.Body.Len() != 0 || w.Header().Get("Content-Length") != index.Header().Get("Content-Length")) {
				t.Fatal("HEAD must describe the index without returning its body")
			}
			if tc.status >= 400 && !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
				t.Fatal("static routing must preserve JSON errors, without an index fallback")
			}
		})
	}
	if !reflect.DeepEqual(before, c.Snapshot()) {
		t.Fatal("console requests changed business state")
	}
}
