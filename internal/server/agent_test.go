package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"remota.local/remota/internal/server/core"
)

func agentRequest(t *testing.T, h *webHarness, token, method, path, body string, edit func(*http.Request)) response {
	t.Helper()
	return h.do(t, method, "/api/v1/agent"+path, body, func(r *http.Request) {
		r.Header.Del("Origin")
		r.Header.Del("Cookie")
		r.Header.Del("X-CSRF-Token")
		r.Header.Set("Authorization", "Bearer "+token)
		if edit != nil {
			edit(r)
		}
	})
}

func TestAgentReadonlyDiagnosticAndIdentity(t *testing.T) {
	h := newWeb(t, nil)
	r := agentRequest(t, h, h.handoff.Access, "POST", "/sessions", "{}", nil)
	checkStatus(t, r, 201)
	session := r.value["session"].(map[string]any)
	sid, token := session["id"].(string), r.value["agent_token"].(string)
	if session["state"] != "pending" || strings.Contains(string(r.body), "hostname") {
		t.Fatal("registration exposed data before consent")
	}
	path := "/sessions/" + sid
	r = agentRequest(t, h, h.handoff.Access, "GET", path, "", nil)
	wantError(t, r, 401, "UNAUTHENTICATED")
	r = agentRequest(t, h, token, "GET", "/sessions/ses_"+strings.Repeat("f", 32), "", nil)
	wantError(t, r, 401, "UNAUTHENTICATED")
	r = agentRequest(t, h, token, "GET", path, "", func(r *http.Request) { r.Header.Set("Origin", "null") })
	wantError(t, r, 403, "ORIGIN_DENIED")
	r = agentRequest(t, h, token, "GET", path, "", func(r *http.Request) { r.Header.Set("Cookie", "unrelated=value") })
	wantError(t, r, 401, "UNAUTHENTICATED")
	r = agentRequest(t, h, token, "GET", path, "", func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+token) })
	wantError(t, r, 401, "UNAUTHENTICATED")
	mcp, err := h.s.Core.AuthenticateCredential(h.handoff.MCP, "mcp")
	must(t, err)
	client, err := h.s.Core.BeginClient(mcp, core.ClientInfo{Name: "test", Version: "1"})
	must(t, err)
	_, err = h.s.Core.ClientMessage(mcp, client.ID, true)
	must(t, err)
	if _, err = h.s.Core.BeginDiagnostic(mcp, client.ID, sid, "ping"); err != core.ConsentRequired {
		t.Fatal("diagnostic accepted before local consent")
	}
	r = agentRequest(t, h, token, "PUT", path+"/decision", `{"decision":"accept","mode":"readonly"}`, nil)
	checkStatus(t, r, 200)
	if _, err = h.s.Core.BeginDiagnostic(mcp, client.ID, sid, "ping"); err != core.AgentUnavailable {
		t.Fatal("diagnostic accepted before the first lease")
	}
	r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
	checkStatus(t, r, 200)
	if r.value["lease"].(map[string]any)["seq"] != float64(1) {
		t.Fatal("initial lease missing")
	}
	p, err := h.s.Core.BeginPoll(sid, 1, nil)
	must(t, err)
	select {
	case <-p.Ready:
		t.Fatal("empty active poll returned immediately")
	default:
	}
	call, err := h.s.Core.BeginDiagnostic(mcp, client.ID, sid, "ping")
	must(t, err)
	select {
	case <-p.Ready:
	default:
		t.Fatal("diagnostic did not wake the pending poll")
	}
	h.s.Core.AbortPoll(p)
	r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
	checkStatus(t, r, 200)
	events := r.value["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["type"] != "call.request" {
		t.Fatal("diagnostic event missing")
	}
	payload := events[0].(map[string]any)["payload"].(map[string]any)
	if payload["call_id"] != call.ID || payload["method"] != "ping" {
		t.Fatal("diagnostic binding differs")
	}
	p, err = h.s.Core.BeginPoll(sid, 1, []string{events[0].(map[string]any)["id"].(string)})
	must(t, err)
	select {
	case <-p.Ready:
		t.Fatal("acknowledged diagnostic remained pending")
	default:
	}
	h.s.Core.AbortPoll(p)
	result := `{"consent_revision":1,"result":{"pong":true,"agent_time":"2026-11-09T04:00:00.000Z"}}`
	resultPath := path + "/calls/" + call.ID + "/result"
	r = agentRequest(t, h, token, "POST", resultPath, result, nil)
	checkStatus(t, r, 204)
	select {
	case <-call.Done:
	default:
		t.Fatal("diagnostic waiter not notified")
	}
	r = agentRequest(t, h, token, "POST", resultPath, result, nil)
	checkStatus(t, r, 204)
	r = agentRequest(t, h, token, "POST", resultPath, strings.Replace(result, "04:00:00", "04:00:01", 1), nil)
	wantError(t, r, 409, "RESULT_CONFLICT")
	got, err := h.s.Core.Diagnostic(mcp, client.ID, sid, call.ID)
	must(t, err)
	if got.Result == nil || !got.Result.Pong {
		t.Fatal("diagnostic result lost")
	}
	h.f.step(10 * time.Second)
	r = agentRequest(t, h, token, "POST", resultPath, result, nil)
	wantError(t, r, 410, "CALL_EXPIRED")
	infoCall, err := h.s.Core.BeginDiagnostic(mcp, client.ID, sid, "info")
	must(t, err)
	r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
	checkStatus(t, r, 200)
	infoResult := `{"consent_revision":1,"result":{"agent_version":"test","os":"linux","arch":"amd64","hostname":"fixture","supported_shells":[]}}`
	infoPath := path + "/calls/" + infoCall.ID + "/result"
	r = agentRequest(t, h, token, "POST", infoPath, infoResult, nil)
	checkStatus(t, r, 204)
	r = agentRequest(t, h, token, "POST", infoPath, infoResult, nil)
	checkStatus(t, r, 204)
	info, err := h.s.Core.Diagnostic(mcp, client.ID, sid, infoCall.ID)
	must(t, err)
	if info.Result == nil || info.Result.Info == nil || info.Result.Info.Hostname != "fixture" {
		t.Fatal("info result lost")
	}
	r = agentRequest(t, h, token, "POST", path+"/close", `{"reason":"local_end"}`, nil)
	checkStatus(t, r, 200)
	r = agentRequest(t, h, token, "PUT", path+"/decision", `{"decision":"accept","mode":"readonly"}`, nil)
	wantError(t, r, 410, "SESSION_ENDED")
	r = agentRequest(t, h, token, "GET", path, "", nil)
	checkStatus(t, r, 200)
	h.f.step(5 * time.Minute)
	r = agentRequest(t, h, token, "GET", path, "", nil)
	wantError(t, r, 401, "UNAUTHENTICATED")
}

func TestAgentDecisionReductionAndPollWake(t *testing.T) {
	h := newWeb(t, nil)
	r := agentRequest(t, h, h.handoff.Access, "POST", "/sessions", "{}", nil)
	checkStatus(t, r, 201)
	sid := r.value["session"].(map[string]any)["id"].(string)
	token := r.value["agent_token"].(string)
	path := "/sessions/" + sid
	r = agentRequest(t, h, token, "PUT", path+"/decision", `{"decision":"accept","mode":"full"}`, nil)
	checkStatus(t, r, 200)
	r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":1,"ack_event_ids":[]}`, nil)
	checkStatus(t, r, 200)
	r = agentRequest(t, h, token, "PUT", path+"/consent", `{"expected_revision":1,"mode":"full"}`, nil)
	wantError(t, r, 403, "LOCAL_RECONSENT_REQUIRED")
	r = agentRequest(t, h, token, "PUT", path+"/consent", `{"expected_revision":0,"mode":"full"}`, nil)
	wantError(t, r, 400, "INVALID_ARGUMENT")
	r = agentRequest(t, h, token, "PUT", path+"/consent", `{"expected_revision":1,"mode":"full","extra":true}`, nil)
	wantError(t, r, 400, "UNKNOWN_FIELD")
	r = agentRequest(t, h, token, "PUT", path+"/consent", `{"expected_revision":2,"mode":"readonly"}`, nil)
	wantError(t, r, 409, "REVISION_CONFLICT")
	r = agentRequest(t, h, token, "PUT", path+"/consent", `{"expected_revision":1,"mode":"readonly"}`, nil)
	checkStatus(t, r, 200)
	if r.value["session"].(map[string]any)["consent"].(map[string]any)["revision"] != float64(2) {
		t.Fatal("reduction revision differs")
	}
	p, err := h.s.Core.BeginPoll(sid, 2, nil)
	must(t, err)
	defer h.s.Core.AbortPoll(p)
	r = agentRequest(t, h, token, "POST", path+"/poll", `{"consent_revision":2,"ack_event_ids":[]}`, nil)
	wantError(t, r, 409, "ALREADY_POLLING")
	_, err = h.s.Core.End(sid, "admin_end")
	must(t, err)
	select {
	case <-p.Ready:
	case <-time.After(time.Second):
		t.Fatal("session end did not wake poll")
	}
	end, err := h.s.Core.CompletePoll(p)
	must(t, err)
	if end.Session.State != "ended" || len(end.Events) != 1 || end.Events[0].Type != "session.end" {
		t.Fatalf("unexpected ended poll: %v", fmt.Sprint(end))
	}
}
