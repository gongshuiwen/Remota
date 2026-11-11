package agent

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remota.local/remota/internal/clock"
	"remota.local/remota/internal/server"
	"remota.local/remota/internal/server/core"
	"remota.local/remota/internal/server/store"
)

func TestReadonlyAgentDiagnosticAndLostReply(t *testing.T) {
	dir := t.TempDir()
	_, handoff, err := store.Initialize(dir, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewUnstartedServer(nil)
	service, err := server.Open(server.Config{StateDir: dir, Listen: web.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	var resultRequests atomic.Int32
	reductionStarted, releaseReduction := make(chan struct{}), make(chan struct{})
	defer close(releaseReduction)
	web.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/consent") {
			io.Copy(io.Discard, r.Body)
			close(reductionStarted)
			select {
			case <-r.Context().Done():
			case <-releaseReduction:
			}
			return
		}
		if strings.Contains(r.URL.Path, "/calls/") && strings.HasSuffix(r.URL.Path, "/result") {
			recorded := httptest.NewRecorder()
			service.Handler().ServeHTTP(recorded, r)
			for key, values := range recorded.Header() {
				w.Header()[key] = append([]string(nil), values...)
			}
			if resultRequests.Add(1) == 1 && recorded.Code == 204 {
				w.WriteHeader(503) // The result committed, but its response was lost.
				return
			}
			w.WriteHeader(recorded.Code)
			io.Copy(w, recorded.Body)
			return
		}
		service.Handler().ServeHTTP(w, r)
	})
	web.Start()
	t.Cleanup(func() { web.Close(); service.Close() })
	transport, err := NewTransport(web.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	source, err := clock.NewReal()
	if err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	probeStarted, releaseProbe := make(chan struct{}), make(chan struct{})
	defer close(releaseProbe)
	a := &Agent{transport: transport, clock: source, info: func() (map[string]any, error) {
		if probes.Add(1) == 2 {
			close(probeStarted)
			<-releaseProbe
		}
		return map[string]any{"agent_version": "test", "os": "linux", "arch": "amd64", "hostname": "fixture", "supported_shells": []string{}}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := make(chan string, 2)
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, handoff.Access, input, &output) }()
	wait := func(condition func() bool) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			if condition() {
				return
			}
			select {
			case err := <-done:
				t.Fatalf("agent stopped early: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
		}
		t.Fatal("agent did not reach expected state")
	}
	var sid string
	wait(func() bool {
		v := service.Core.Snapshot()
		if len(v.Sessions) != 1 || v.Sessions[0].State != "pending" {
			return false
		}
		sid = v.Sessions[0].ID
		return true
	})
	if probes.Load() != 0 {
		t.Fatal("system information read before local consent")
	}
	input <- ""
	wait(func() bool {
		v, err := service.Core.GetSession(sid)
		return err == nil && v.State == "active" && v.Consent.Mode == "readonly" && v.Activity == "recent"
	})
	credential, err := service.Core.AuthenticateCredential(handoff.MCP, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	client, err := service.Core.BeginClient(credential, core.ClientInfo{Name: "test", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Core.ClientMessage(credential, client.ID, true); err != nil {
		t.Fatal(err)
	}
	call, err := service.Core.BeginDiagnostic(credential, client.ID, sid, "info")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-call.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("diagnostic did not complete")
	}
	wait(func() bool { return resultRequests.Load() >= 2 })
	result, err := service.Core.Diagnostic(credential, client.ID, sid, call.ID)
	if err != nil || result.Result == nil || result.Result.Info == nil || result.Result.Info.Hostname != "fixture" || probes.Load() != 1 {
		t.Fatalf("unexpected diagnostic or repeated probe: %v", err)
	}
	ping, err := service.Core.BeginDiagnostic(credential, client.ID, sid, "ping")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ping.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("ping did not complete")
	}
	pingResult, err := service.Core.Diagnostic(credential, client.ID, sid, ping.ID)
	if err != nil || pingResult.Result == nil || !pingResult.Result.Pong || probes.Load() != 1 {
		t.Fatalf("unexpected ping result: %v", err)
	}
	_, _, err = service.Core.Submit(credential, client.ID, sid, strings.Repeat("1", 32), core.Spec{Command: "echo denied", Reason: "test", Shell: "sh", TimeoutSeconds: 10})
	if err != core.ConsentRequired {
		t.Fatalf("read-only session accepted command: %v", err)
	}
	if _, err = service.Core.BeginDiagnostic(credential, client.ID, sid, "info"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second diagnostic did not start")
	}
	input <- "r"
	select {
	case <-reductionStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("reduction request did not start")
	}
	input <- "q"
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local end did not stop the agent")
	}
	session, err := service.Core.GetSession(sid)
	if err != nil || session.State != "ended" || session.EndReason != "local_end" {
		t.Fatalf("session did not end locally: %v", err)
	}
	if strings.Contains(output.String(), handoff.Access) {
		t.Fatal("access credential was printed")
	}
}

func TestDiagnosticEventDeduplicationAndSessionBinding(t *testing.T) {
	state, source, _, _, _ := activeState(t)
	first, second, other := fixture(t, "poll.readonly"), fixture(t, "poll.readonly"), fixture(t, "poll.readonly")
	first["lease"].(map[string]any)["seq"] = stdjson.Number("2")
	second["lease"].(map[string]any)["seq"] = stdjson.Number("3")
	other["lease"].(map[string]any)["seq"] = stdjson.Number("4")
	for i, response := range []map[string]any{first, second} {
		event := fixture(t, "event.call")
		payload := event["payload"].(map[string]any)
		payload["method"] = "info"
		if i == 1 {
			payload["consent_revision"] = stdjson.Number("1.0")
		}
		response["events"] = []any{event}
	}
	event := fixture(t, "event.call")
	event["id"], event["session_id"] = "evt_"+strings.Repeat("d", 32), "ses_"+strings.Repeat("f", 32)
	event["payload"].(map[string]any)["call_id"] = "call_" + strings.Repeat("b", 32)
	event["payload"].(map[string]any)["method"] = "info"
	other["events"] = []any{event}
	var polls, results, probes atomic.Int32
	release := make(chan struct{})
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Remota-Epoch", strings.Repeat("a", 32))
		w.Header().Set("X-Request-ID", "request-identifier")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/poll"):
			switch polls.Add(1) {
			case 1:
				json.MarshalWrite(w, first)
			case 2:
				json.MarshalWrite(w, second)
			case 3:
				json.MarshalWrite(w, other)
			default:
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}
		case strings.HasSuffix(r.URL.Path, "/result"):
			w.WriteHeader(204)
			results.Add(1)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer web.Close()
	defer close(release)
	transport, err := NewTransport(web.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{transport: transport, clock: source.now, info: func() (map[string]any, error) {
		probes.Add(1)
		return map[string]any{"agent_version": "test", "os": "linux", "arch": "amd64", "hostname": "fixture", "supported_shells": []string{}}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = a.poll(ctx, state, state.session.id, "token", strings.Repeat("a", 32))
	if !errors.Is(err, ErrProtocol) || state.phase != "ended" || probes.Load() != 1 || results.Load() != 2 {
		t.Fatalf("event handling: err=%v probes=%d results=%d", err, probes.Load(), results.Load())
	}
}

func TestWatchdogStopsBlockedPollWithoutReregistering(t *testing.T) {
	registration := fixture(t, "registration")
	readonly := fixture(t, "poll.readonly")
	decision := map[string]any{"session": readonly["session"], "activation_remaining_ms": 15000}
	ended := map[string]any{"session": fixture(t, "poll.ended")["session"]}
	var registrations, polls atomic.Int32
	blocked, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Remota-Epoch", strings.Repeat("a", 32))
		w.Header().Set("X-Request-ID", strings.Repeat("b", 32))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/agent/sessions":
			registrations.Add(1)
			w.WriteHeader(201)
			json.MarshalWrite(w, registration)
		case strings.HasSuffix(r.URL.Path, "/decision"):
			json.MarshalWrite(w, decision)
		case strings.HasSuffix(r.URL.Path, "/poll"):
			if polls.Add(1) == 1 {
				json.MarshalWrite(w, readonly)
				return
			}
			close(blocked)
			select {
			case <-r.Context().Done():
				close(cancelled)
			case <-release:
			}
		case strings.HasSuffix(r.URL.Path, "/close"):
			json.MarshalWrite(w, ended)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer web.Close()
	defer close(release)
	transport, err := NewTransport(web.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	f := &testClock{n: clock.Sample{Wall: time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC)}}
	a := &Agent{transport: transport, clock: f.now, info: func() (map[string]any, error) {
		t.Fatal("diagnostic ran without an event")
		return nil, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := make(chan string, 1)
	input <- ""
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, "rm_access_"+strings.Repeat("A", 43), input, io.Discard) }()
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not enter the long poll")
	}
	f.advance(0, 30*time.Second)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog was blocked by the network request")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("expired poll was not cancelled")
	}
	if registrations.Load() != 1 {
		t.Fatal("agent registered a new session after expiry")
	}
}

func TestServerEpochChangeEndsOldAgent(t *testing.T) {
	registration := fixture(t, "registration")
	readonly := fixture(t, "poll.readonly")
	decision := map[string]any{"session": readonly["session"], "activation_remaining_ms": 15000}
	ended := map[string]any{"session": fixture(t, "poll.ended")["session"]}
	var registrations, polls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", strings.Repeat("c", 32))
		w.Header().Set("Content-Type", "application/json")
		epoch := strings.Repeat("a", 32)
		if strings.HasSuffix(r.URL.Path, "/poll") && polls.Add(1) > 1 {
			epoch = strings.Repeat("b", 32)
		}
		w.Header().Set("X-Remota-Epoch", epoch)
		switch {
		case r.URL.Path == "/api/v1/agent/sessions":
			registrations.Add(1)
			w.WriteHeader(201)
			json.MarshalWrite(w, registration)
		case strings.HasSuffix(r.URL.Path, "/decision"):
			json.MarshalWrite(w, decision)
		case strings.HasSuffix(r.URL.Path, "/poll"):
			json.MarshalWrite(w, readonly)
		case strings.HasSuffix(r.URL.Path, "/close"):
			json.MarshalWrite(w, ended)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer web.Close()
	transport, err := NewTransport(web.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	f := &testClock{n: clock.Sample{Wall: time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC)}}
	a := &Agent{transport: transport, clock: f.now, info: func() (map[string]any, error) {
		t.Fatal("diagnostic ran without an event")
		return nil, nil
	}}
	input := make(chan string, 1)
	input <- ""
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = a.Run(ctx, "rm_access_"+strings.Repeat("A", 43), input, io.Discard)
	if !errors.Is(err, ErrProtocol) || registrations.Load() != 1 {
		t.Fatalf("epoch change did not terminate the original session: %v", err)
	}
}
