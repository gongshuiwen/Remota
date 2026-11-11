package core

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNearExpiryRegistrationAndActivation(t *testing.T) {
	h := newHarness(t, 0, 30*time.Second)
	s, err := h.c.GetSession(h.sid)
	must(t, err)
	if s.ExpiresAt.Sub(s.CreatedAt) != 30*time.Second || !s.DecisionDeadline.Equal(s.ExpiresAt) {
		t.Fatalf("%+v", s)
	}
	h.f.step(25 * time.Second)
	_, err = h.c.Decide(h.sid, "accept", "readonly")
	must(t, err)
	p := h.poll(t, 1, nil)
	if p.Lease.ValidForMS != 5000 || p.SessionRemainingMS != 5000 {
		t.Fatalf("%+v", p)
	}
	h.f.step(5 * time.Second)
	p = h.poll(t, 1, nil)
	if p.Lease != nil || p.Session.State != "ended" || p.Session.EndReason != "access_invalid" {
		t.Fatalf("%+v", p)
	}
	_, _, err = h.c.Register(h.access)
	wantCode(t, err, Unauthenticated)
	_, err = h.c.Decide(h.sid, "accept", "readonly")
	wantCode(t, err, SessionEnded)
	if len(h.c.Snapshot().Sessions) != 1 {
		t.Fatal("expired registration created a session")
	}
}

func TestDeadlineOrderAndFirstCause(t *testing.T) {
	for _, accessFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("session_boundary_access_%v", accessFirst), func(t *testing.T) {
			life := 24 * time.Hour
			if accessFirst {
				life = time.Minute
			}
			h := newHarness(t, time.Minute, life)
			h.activate(t, "full")
			h.spec.TimeoutSeconds = 60
			task := h.submit(t, 1)
			g := h.grant(t, task)
			h.started(t, task, g)
			h.f.step(20 * time.Second)
			h.poll(t, 1, nil)
			h.f.step(20 * time.Second)
			h.poll(t, 1, nil)
			h.f.step(20 * time.Second)
			s, err := h.c.GetSession(h.sid)
			must(t, err)
			got := h.get(t, task)
			reason, stop := "session_expired", "session_ended"
			if accessFirst {
				reason, stop = "access_invalid", "credential_invalid"
			}
			if s.EndReason != reason || got.Stop.Reason != stop || got.Stop.ConfirmationDeadline.Sub(*s.EndedAt) != 5*time.Second {
				t.Fatalf("session=%+v task=%+v", s, got)
			}
			_, err = h.c.RecordResult(h.sid, task.ID, fullResult(g, "succeeded", ptr(0)))
			wantCode(t, err, SessionEnded)
			got, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID, ExitCode: ptr(0)})
			must(t, err)
			if got.Outcome != "cancelled" {
				t.Fatalf("%+v", got)
			}
		})
	}
	t.Run("lease_and_execution_equal", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		h.spec.TimeoutSeconds = 30
		task := h.submit(t, 1)
		g := h.grant(t, task)
		h.started(t, task, g)
		h.f.step(30 * time.Second)
		got := h.get(t, task)
		if got.Stop.Reason != "session_ended" {
			t.Fatalf("%+v", got)
		}
		s, err := h.c.GetSession(h.sid)
		must(t, err)
		if s.EndReason != "lease_expired" {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("overdue_execution_before_later_lease", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		g := h.grant(t, task)
		h.started(t, task, g)
		h.f.step(35 * time.Second)
		now := h.f.now().Wall
		got := h.get(t, task)
		if got.Stop.Reason != "execution_deadline" || !got.Stop.RequestedAt.Equal(now) || !got.Stop.ConfirmationDeadline.Equal(now.Add(5*time.Second)) {
			t.Fatalf("%+v", got)
		}
		v, err := h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID, ExitCode: ptr(0)})
		must(t, err)
		if v.Outcome != "timed_out" {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("existing_stop_is_fixed", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		h.spec.TimeoutSeconds = 30
		task := h.submit(t, 1)
		g := h.grant(t, task)
		h.started(t, task, g)
		h.f.step(29 * time.Second)
		old, err := h.c.Cancel(h.credential, h.client, h.sid, task.ID)
		must(t, err)
		h.f.step(time.Second)
		got := h.get(t, task)
		if !reflect.DeepEqual(old.Stop, got.Stop) {
			t.Fatal("deadline overwrote accepted stop")
		}
	})
	t.Run("credential_and_client_expiry", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		id := h.issueCredential(t, "mcp", true, time.Minute)
		h.f.step(50 * time.Second)
		h.activate(t, "full")
		cl, err := h.c.BeginClient(id, ClientInfo{Name: "test", Version: "1"})
		must(t, err)
		_, err = h.c.ClientMessage(id, cl.ID, true)
		must(t, err)
		h.credential, h.client = id, cl.ID
		task := h.submit(t, 1)
		g := h.grant(t, task)
		h.started(t, task, g)
		h.f.step(10 * time.Second)
		snap := h.c.Snapshot()
		if snap.Tasks[0].Stop.Reason != "credential_invalid" || snap.Sessions[0].State != "active" {
			t.Fatalf("%+v", snap)
		}
		found := false
		for _, client := range snap.Clients {
			if client.ID == cl.ID {
				found = true
				if client.EndReason != "credential_invalid" {
					t.Fatal("wrong client cause")
				}
			}
		}
		if !found {
			t.Fatal("missing ended client")
		}
	})
}

func TestClockRollbackForwardAndSuspend(t *testing.T) {
	for _, tc := range []struct {
		name          string
		wall, elapsed time.Duration
	}{
		{"wall_rollback", -time.Hour, 30 * time.Second},
		{"wall_forward", 30 * time.Second, 0},
		{"suspend", 30 * time.Second, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 0, 0)
			h.activate(t, "full")
			h.spec.TimeoutSeconds = 120
			task := h.submit(t, 1)
			g := h.grant(t, task)
			h.started(t, task, g)
			h.f.advance(tc.wall, tc.elapsed)
			p := h.poll(t, 1, nil)
			if p.Lease != nil || p.Session.EndReason != "lease_expired" {
				t.Fatalf("%+v", p)
			}
			if h.get(t, task).Execution.State == "exited" {
				t.Fatal("inferred OS exit from clock expiration")
			}
		})
	}
	t.Run("execution_budget_not_reanchored", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		g := h.grant(t, task)
		h.f.advance(-time.Hour, 4*time.Second)
		_, again, err := h.c.Start(h.sid, task.ID, 1)
		must(t, err)
		if g != again {
			t.Fatal("wall rollback reanchored grant")
		}
		h.f.advance(0, time.Second)
		_, _, err = h.c.Start(h.sid, task.ID, 1)
		wantCode(t, err, GrantExpired)
	})
	t.Run("elapsed_clock_regression_fails_closed", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		admin, err := h.c.Login("")
		must(t, err)
		h.f.step(time.Second)
		h.c.Sweep()
		h.f.advance(0, -time.Second)
		s := h.c.Snapshot()
		if s.Status != "degraded" || s.Sessions[0].State != "ended" {
			t.Fatalf("%+v", s)
		}
		_, _, err = h.c.AdminState(admin.Token)
		wantCode(t, err, Unauthenticated)
	})
}

func TestPollValidationWaitingAndCleanupEvents(t *testing.T) {
	h := newHarness(t, 0, 0)
	h.activate(t, "full")
	task := h.submit(t, 1)
	for range 16 {
		_, err := h.c.BeginDiagnostic(h.credential, h.client, h.sid, "ping")
		must(t, err)
	}
	p := h.poll(t, 1, nil)
	if len(p.Events) != 16 {
		t.Fatalf("diagnostic batch size = %d", len(p.Events))
	}
	diagnosticACKs := make([]string, 0, len(p.Events))
	for _, event := range p.Events {
		if event.Type != "call.request" {
			t.Fatal("diagnostic priority lost when batch was truncated")
		}
		diagnosticACKs = append(diagnosticACKs, event.ID)
	}
	p = h.poll(t, 1, diagnosticACKs)
	offer := p.Events[0]
	if offer.Type != "task.offer" || offer.Task.Spec == nil {
		t.Fatalf("%+v", offer)
	}
	// Mutation of returned events cannot rewrite the immutable offer cache.
	offer.Task.Spec.Command = "tampered"
	p = h.poll(t, 1, nil)
	if p.Events[0].Task.Spec.Command != h.spec.Command {
		t.Fatal("event alias")
	}
	h.f.step(time.Second)
	_, err := h.c.BeginPoll(h.sid, 1, []string{p.Events[0].ID, "bad"})
	wantCode(t, err, InvalidArgument)
	p = h.poll(t, 1, nil)
	if len(p.Events) != 1 {
		t.Fatal("invalid ack partially acknowledged offer")
	}
	first, err := h.c.BeginPoll(h.sid, 1, nil)
	must(t, err)
	_, err = h.c.BeginPoll(h.sid, 1, nil)
	wantCode(t, err, AlreadyPolling)
	h.c.AbortPoll(first)
	_, err = h.c.CompletePoll(first)
	wantCode(t, err, InvalidState)
	g := h.grant(t, task)
	h.started(t, task, g)
	_, err = h.c.End(h.sid, "admin_end")
	must(t, err)
	p = h.poll(t, 1, nil)
	if p.Lease != nil || len(p.Events) != 2 || p.Events[0].Type != "session.end" || p.Events[1].Type != "task.cancel" {
		t.Fatalf("%+v", p)
	}
	ack := []string{p.Events[0].ID, p.Events[1].ID}
	oldCancel := p.Events[1]
	p = h.poll(t, 0, ack)
	if len(p.Events) != 1 || !reflect.DeepEqual(p.Events[0], oldCancel) {
		t.Fatal("ack suppressed or changed cancel")
	}
	h.f.step(5 * time.Second)
	p = h.poll(t, 0, ack)
	if len(p.Events) != 1 || p.Session.Cleanup != "unknown" {
		t.Fatal("unknown cleanup lost cancel")
	}
	_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID})
	must(t, err)
	p = h.poll(t, 0, ack)
	if len(p.Events) != 0 || p.Session.Cleanup != "confirmed" {
		t.Fatalf("%+v", p)
	}
}

func TestPollCompletionRechecksLeaseAndRevision(t *testing.T) {
	for _, action := range []string{"expired", "reduced", "aborted"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t, 0, 0)
			h.activate(t, "full")
			p, err := h.c.BeginPoll(h.sid, 1, nil)
			must(t, err)
			switch action {
			case "expired":
				h.f.step(30 * time.Second)
			case "reduced":
				_, err = h.c.Reduce(h.sid, 1, "readonly")
				must(t, err)
			case "aborted":
				h.c.AbortPoll(p)
			}
			got, err := h.c.CompletePoll(p)
			switch action {
			case "expired":
				must(t, err)
				if got.Lease != nil || got.Session.State != "ended" {
					t.Fatalf("%+v", got)
				}
			case "reduced":
				wantCode(t, err, RevisionConflict)
			case "aborted":
				wantCode(t, err, InvalidState)
			}
		})
	}
}

func TestReductionClientEndDegradedAndRetention(t *testing.T) {
	t.Run("reduce_does_not_restore_initial_full", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		g := h.grant(t, task)
		s, err := h.c.Reduce(h.sid, 1, "readonly")
		must(t, err)
		_, err = h.c.Reduce(h.sid, 1, "readonly")
		must(t, err)
		s, err = h.c.Decide(h.sid, "accept", "full")
		must(t, err)
		if s.Consent.Mode != "readonly" || s.Consent.Revision != 2 {
			t.Fatalf("%+v", s)
		}
		_, err = h.c.Reduce(h.sid, 2, "full")
		wantCode(t, err, Reconsent)
		_, err = h.c.RecordResult(h.sid, task.ID, fullResult(g, "succeeded", ptr(0)))
		wantCode(t, err, RevisionConflict)
		v, err := h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "not_started", Reason: "LOCAL_PERMISSION_DENIED", GrantID: g.ID})
		must(t, err)
		if v.Outcome != "cancelled" || v.Stop.Reason != "local_revoked" {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("new_client_does_not_rebind_task", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		_, err := h.c.EndClient(h.client, "client_delete")
		must(t, err)
		cl, err := h.c.BeginClient(h.credential, ClientInfo{Name: "test", Version: "1"})
		must(t, err)
		_, err = h.c.ClientMessage(h.credential, cl.ID, true)
		must(t, err)
		v, reused, err := h.c.Submit(h.credential, cl.ID, h.sid, task.RequestID, h.spec)
		must(t, err)
		if !reused || v.ClientID != h.client || v.Outcome != "cancelled" {
			t.Fatalf("%+v", v)
		}
		_, _, err = h.c.Start(h.sid, task.ID, 1)
		wantCode(t, err, ClientNotFound)
	})
	t.Run("degraded_cannot_resume", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		g := h.grant(t, task)
		h.started(t, task, g)
		must(t, h.c.Halt("degraded"))
		_, _, err := h.c.Register(h.access)
		wantCode(t, err, StateUnavailable)
		p := h.poll(t, 1, nil)
		if p.Lease != nil || p.Session.EndReason != "server_degraded" {
			t.Fatalf("%+v", p)
		}
		_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID})
		must(t, err)
		if h.c.Snapshot().Status != "degraded" {
			t.Fatal("cleanup restored authority")
		}
	})
	t.Run("retention_is_absolute_and_redacts_all_caches", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		g := h.grant(t, task)
		_, err := h.c.RecordResult(h.sid, task.ID, fullResult(g, "succeeded", ptr(0)))
		must(t, err)
		_, err = h.c.End(h.sid, "local_end")
		must(t, err)
		v := h.get(t, task)
		if v.Spec != nil || v.Result != nil || v.DetailsAvailable {
			t.Fatalf("%+v", v)
		}
		// Privacy also covers copies retained for duplicate handling and delivery.
		cached := h.c.sessions[h.sid].tasks[task.ID]
		if cached.grant.Spec != (Spec{}) || cached.result != nil || cached.offerEvent != nil {
			t.Fatal("ended session kept private cache")
		}
		h.f.step(299 * time.Second)
		h.poll(t, 0, nil)
		h.f.step(time.Second)
		_, err = h.c.GetSession(h.sid)
		wantCode(t, err, NotFound)
		_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID})
		wantCode(t, err, NotFound)
	})
}

func TestConcurrentBoundariesAndSnapshotIsolation(t *testing.T) {
	h := newHarness(t, 0, 0)
	h.activate(t, "full")
	var wg sync.WaitGroup
	ids := make(chan string, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, _, err := h.c.Submit(h.credential, h.client, h.sid, strings.Repeat("1", 32), h.spec)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- v.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for next := range ids {
		if id != "" && next != id {
			t.Fatal("duplicate tasks")
		}
		id = next
	}
	task := h.c.Snapshot().Tasks[0]
	if task.ID != id || len(h.c.Snapshot().Tasks) != 1 {
		t.Fatal("incoherent snapshot")
	}
	g := h.grant(t, task)
	for _, action := range []func(){
		func() {
			_, err := h.c.Cancel(h.credential, h.client, h.sid, task.ID)
			if err != nil {
				t.Error(err)
			}
		},
		func() {
			_, err := h.c.RecordResult(h.sid, task.ID, fullResult(g, "succeeded", ptr(0)))
			if err != nil {
				t.Error(err)
			}
		},
		func() {
			for i := 0; i < 50; i++ {
				snap := h.c.Snapshot()
				if len(snap.Tasks) != 1 {
					t.Error("partial snapshot")
				}
			}
		},
	} {
		wg.Add(1)
		go func(f func()) { defer wg.Done(); f() }(action)
	}
	wg.Wait()
	v := h.get(t, task)
	if v.Outcome != "succeeded" && v.Outcome != "cancelled" {
		t.Fatalf("%+v", v)
	}
	if v.Stop != nil && v.Outcome != "cancelled" {
		t.Fatal("result ignored earlier cancellation")
	}
	snap := h.c.Snapshot()
	snap.Sessions[0].Consent.Mode = "tampered"
	snap.Tasks[0].Execution.ExitCode = ptr(9)
	*v.Execution.ExitCode = 9
	v.Spec.Command = "tampered"
	if h.get(t, task).Spec.Command != h.spec.Command || *h.get(t, task).Execution.ExitCode != 0 || h.c.Snapshot().Sessions[0].Consent.Mode != "full" {
		t.Fatal("snapshot aliases state")
	}
}

func TestConcurrentRevokeAndStart(t *testing.T) {
	for i := 0; i < 20; i++ {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		var wg sync.WaitGroup
		wg.Add(2)
		var startErr error
		go func() { defer wg.Done(); _, _, startErr = h.c.Start(h.sid, task.ID, 1) }()
		go func() {
			defer wg.Done()
			if err := h.revokeCredential(h.access); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		v := h.get(t, task)
		if startErr == nil {
			if v.Phase != "stopping" || v.Execution.State != "may_have_started" {
				t.Fatalf("%+v", v)
			}
		} else {
			wantCode(t, startErr, SessionEnded)
			if v.Phase != "settled" || v.Execution.State != "not_started" {
				t.Fatalf("%+v", v)
			}
		}
	}
}
