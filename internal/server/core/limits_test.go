package core

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestActivationAndClientLifecycle(t *testing.T) {
	t.Run("decision_timeout", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.f.step(120 * time.Second)
		_, err := h.c.Decide(h.sid, "accept", "full")
		wantCode(t, err, SessionEnded)
		s, err := h.c.GetSession(h.sid)
		must(t, err)
		if s.EndReason != "decision_timeout" {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("first_poll_required_and_not_renewed_by_decide", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		_, _, err := h.c.Submit(h.credential, h.client, h.sid, strings.Repeat("1", 32), h.spec)
		wantCode(t, err, ConsentRequired)
		_, err = h.c.Decide(h.sid, "accept", "full")
		must(t, err)
		_, _, err = h.c.Submit(h.credential, h.client, h.sid, strings.Repeat("1", 32), h.spec)
		wantCode(t, err, AgentUnavailable)
		h.f.step(14 * time.Second)
		_, err = h.c.Decide(h.sid, "accept", "full")
		must(t, err)
		h.f.step(time.Second)
		p, err := h.c.BeginPoll(h.sid, 1, nil)
		must(t, err)
		v, err := h.c.CompletePoll(p)
		must(t, err)
		if v.Lease != nil || v.Session.EndReason != "lease_expired" {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("init_timeout_is_not_idle_timeout", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		cl, err := h.c.BeginClient(h.credential)
		must(t, err)
		if !cl.LastSeenAt.Equal(cl.CreatedAt) || !cl.ExpiresAt.Equal(cl.CreatedAt.Add(30*time.Second)) {
			t.Fatalf("invalid initializing timestamps: %+v", cl)
		}
		_, err = h.c.ClientMessage(h.credential, cl.ID, false)
		wantCode(t, err, InvalidState)
		h.f.step(30 * time.Second)
		_, err = h.c.ClientMessage(h.credential, cl.ID, true)
		wantCode(t, err, ClientNotFound)
		found := false
		for _, v := range h.c.Snapshot().Clients {
			if v.ID == cl.ID {
				found = true
				if v.EndReason != "init_timeout" {
					t.Fatalf("%+v", v)
				}
			}
		}
		if !found {
			t.Fatal("ended metadata missing")
		}
	})
	t.Run("idle_deadline_slides_only_on_authenticated_message", func(t *testing.T) {
		h := newHarness(t, 8*time.Hour, 0)
		h.f.step(29 * time.Minute)
		cl, err := h.c.ClientMessage(h.credential, h.client, false)
		must(t, err)
		if !cl.LastSeenAt.Equal(h.f.now().Wall) || !cl.ExpiresAt.Equal(cl.LastSeenAt.Add(30*time.Minute)) {
			t.Fatalf("invalid ready timestamps: %+v", cl)
		}
		h.f.step(time.Minute)
		observed := h.c.Snapshot().Clients[0]
		if observed.Lifecycle != "ready" {
			t.Fatal("valid message did not refresh idle deadline")
		}
		if !observed.LastSeenAt.Equal(cl.LastSeenAt) || !observed.ExpiresAt.Equal(cl.ExpiresAt) {
			t.Fatal("snapshot changed client timestamps")
		}
		_, err = h.c.ClientMessage("crd_"+strings.Repeat("f", 32), h.client, false)
		wantCode(t, err, Unauthenticated)
		h.f.step(29 * time.Minute)
		_, err = h.c.ClientMessage(h.credential, h.client, false)
		wantCode(t, err, ClientNotFound)
		ended := h.c.Snapshot().Clients[0]
		if ended.EndReason != "idle_timeout" {
			t.Fatal("wrong expiry cause")
		}
		if !ended.LastSeenAt.Equal(cl.LastSeenAt) || !ended.ExpiresAt.Equal(cl.ExpiresAt) {
			t.Fatal("ended metadata changed client timestamps")
		}
	})
}

func TestTaskQuotaDoesNotEvictIdempotence(t *testing.T) {
	h := newHarness(t, 0, 0)
	h.activate(t, "full")
	first := h.submit(t, 1)
	_, _, err := h.c.Submit(h.credential, h.client, h.sid, fmt.Sprintf("%032x", 2), h.spec)
	wantCode(t, err, TaskBusy)
	for i := 1; i <= 32; i++ {
		task := first
		if i > 1 {
			task = h.submit(t, i)
		}
		_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "not_started", Reason: "GRANT_UNAVAILABLE"})
		must(t, err)
	}
	_, _, err = h.c.Submit(h.credential, h.client, h.sid, fmt.Sprintf("%032x", 33), h.spec)
	wantCode(t, err, TaskLimit)
	v, reused, err := h.c.Submit(h.credential, h.client, h.sid, first.RequestID, h.spec)
	must(t, err)
	if !reused || v.ID != first.ID || len(h.c.Snapshot().Tasks) != 32 {
		t.Fatal("quota evicted intent")
	}
}

func TestOfferExpiryKeepsActiveSession(t *testing.T) {
	h := newHarness(t, 0, 0)
	h.activate(t, "full")
	task := h.submit(t, 1)
	for i := 0; i < 2; i++ {
		h.f.step(20 * time.Second)
		h.poll(t, 1, nil)
	}
	h.f.step(20 * time.Second)
	v := h.get(t, task)
	if v.Outcome != "timed_out" || v.Execution.State != "not_started" {
		t.Fatalf("%+v", v)
	}
	s, err := h.c.GetSession(h.sid)
	must(t, err)
	if s.State != "active" {
		t.Fatal("offer timeout ended valid session")
	}
}

func TestReadOnlyMCPAuthorityAndReceiptRecovery(t *testing.T) {
	h := newHarness(t, 0, 0)
	h.activate(t, "full")
	task := h.submit(t, 1)
	g := h.grant(t, task)
	readCredential := "crd_" + strings.Repeat("e", 32)
	must(t, h.c.LoadCredential(Credential{ID: readCredential, Kind: "mcp", ExpiresAt: h.f.now().Wall.Add(time.Hour)}))
	cl, err := h.c.BeginClient(readCredential)
	must(t, err)
	_, err = h.c.ClientMessage(readCredential, cl.ID, true)
	must(t, err)
	v, err := h.c.GetTask(readCredential, cl.ID, h.sid, "", task.RequestID)
	must(t, err)
	if v.Spec == nil || *v.Spec != h.spec {
		t.Fatal("read-only MCP cannot inspect authorized task")
	}
	_, _, err = h.c.Submit(readCredential, cl.ID, h.sid, strings.Repeat("2", 32), h.spec)
	wantCode(t, err, ExecNotAllowed)
	_, err = h.c.Cancel(readCredential, cl.ID, h.sid, task.ID)
	must(t, err)
	// Unconfirmed recovery ends the old session immediately, without waiting.
	v, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "stop_unconfirmed", GrantID: g.ID, ErrorCode: "PROCESS_STOP_UNCONFIRMED"})
	must(t, err)
	if v.Outcome != "unknown" || v.Execution.State != "unknown" {
		t.Fatalf("%+v", v)
	}
	s, err := h.c.GetSession(h.sid)
	must(t, err)
	if s.EndReason != "execution_unknown" {
		t.Fatalf("%+v", s)
	}
	h.f.step(time.Millisecond)
	v = h.started(t, task, g)
	if v.Execution.State != "unknown" || v.Outcome != "unknown" {
		t.Fatal("late started revived task")
	}
	_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "not_started", Reason: "CANCELLED_BEFORE_SPAWN"})
	wantCode(t, err, ResultConflict)
	v, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID})
	must(t, err)
	if v.Execution.State != "exited" || v.Outcome != "unknown" {
		t.Fatal("exit changed unknown outcome")
	}
}

func TestCommandAndResultBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Spec)
	}{
		{"empty", func(s *Spec) { s.Command = "" }},
		{"white_space", func(s *Spec) { s.Command = "\t\n\u0085\u00a0\u1680\u2000\u2028\u202f\u205f\u3000" }},
		{"nul", func(s *Spec) { s.Command = "a\x00b" }},
		{"invalid_utf8", func(s *Spec) { s.Command = string([]byte{0xff}) }},
		{"byte_limit", func(s *Spec) { s.Command = strings.Repeat("界", 1366) }},
		{"empty_reason", func(s *Spec) { s.Reason = "\u200a" }},
		{"reason_bytes", func(s *Spec) { s.Reason = strings.Repeat("界", 171) }},
		{"shell", func(s *Spec) { s.Shell = "bash" }},
		{"timeout_low", func(s *Spec) { s.TimeoutSeconds = 0 }},
		{"timeout_high", func(s *Spec) { s.TimeoutSeconds = 121 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 0, 0)
			h.activate(t, "full")
			s := h.spec
			tc.edit(&s)
			_, _, err := h.c.Submit(h.credential, h.client, h.sid, strings.Repeat("1", 32), s)
			wantCode(t, err, InvalidArgument)
			if len(h.c.Snapshot().Tasks) != 0 {
				t.Fatal("invalid spec changed state")
			}
		})
	}
	t.Run("unicode_values_are_preserved", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		h.spec.Command = " \x1c\u200b\n"
		h.spec.Reason = " reason "
		task := h.submit(t, 1)
		g := h.grant(t, task)
		if g.Spec != h.spec {
			t.Fatal("command/reason was trimmed or normalized")
		}
	})
	for _, tc := range []struct {
		name string
		edit func(*Result)
	}{
		{"success_missing_code", func(r *Result) { r.ExitCode = nil; r.ErrorCode = "EXIT_CODE_UNAVAILABLE" }},
		{"failed_zero", func(r *Result) { r.Outcome = "failed" }},
		{"missing_code_without_error", func(r *Result) { r.ExitCode = nil }},
		{"code_with_missing_error", func(r *Result) { r.ErrorCode = "EXIT_CODE_UNAVAILABLE" }},
		{"negative_capture", func(r *Result) { r.Stdout.CapturedBytes = -1 }},
		{"capture_limit", func(r *Result) { r.Stderr.CapturedBytes = 8193 }},
		{"text_byte_limit", func(r *Result) { r.Stdout.Text = strings.Repeat("界", 8193) }},
		{"unknown_result", func(r *Result) { r.Outcome = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 0, 0)
			h.activate(t, "full")
			task := h.submit(t, 1)
			g := h.grant(t, task)
			r := fullResult(g, "succeeded", ptr(0))
			tc.edit(&r)
			_, err := h.c.RecordResult(h.sid, task.ID, r)
			wantCode(t, err, InvalidArgument)
			if h.get(t, task).Phase != "start_granted" {
				t.Fatal("invalid result settled task")
			}
		})
	}
}
