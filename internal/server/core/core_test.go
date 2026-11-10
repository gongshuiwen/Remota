package core

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"remota.local/remota/internal/auth"
	"remota.local/remota/internal/clock"
)

type testClock struct {
	mu sync.Mutex
	n  clock.Sample
}

func (f *testClock) now() clock.Sample { f.mu.Lock(); defer f.mu.Unlock(); return f.n }
func (f *testClock) advance(wall, elapsed time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n.Wall = f.n.Wall.Add(wall)
	f.n.Elapsed += elapsed
}
func (f *testClock) step(d time.Duration) { f.advance(d, d) }

type harness struct {
	c                               *Core
	f                               *testClock
	sid, access, credential, client string
	spec                            Spec
}

var messageFixtures = sync.OnceValues(func() (map[string]json.RawMessage, error) {
	b, err := os.ReadFile("../../../docs/contracts/acceptance/messages.json")
	if err != nil {
		return nil, err
	}
	var corpus struct {
		Valid []struct {
			ID    string
			Value json.RawMessage
		}
	}
	if err = json.Unmarshal(b, &corpus); err != nil {
		return nil, err
	}
	values := make(map[string]json.RawMessage, len(corpus.Valid))
	for _, f := range corpus.Valid {
		values[f.ID] = f.Value
	}
	return values, nil
})

func fixture(t *testing.T, id string) json.RawMessage {
	t.Helper()
	values, err := messageFixtures()
	must(t, err)
	value, ok := values[id]
	if !ok {
		t.Fatalf("fixture %s not found", id)
	}
	return append(json.RawMessage(nil), value...)
}

func expectedTask(t *testing.T, got Task, id string) {
	t.Helper()
	var want struct {
		Phase, Outcome   string
		Execution        struct{ State string }
		DetailsAvailable bool `json:"details_available"`
	}
	if err := json.Unmarshal(fixture(t, id), &want); err != nil {
		t.Fatal(err)
	}
	if got.Phase != want.Phase || got.Outcome != want.Outcome || got.Execution.State != want.Execution.State || got.DetailsAvailable != want.DetailsAvailable {
		t.Fatalf("%s: got phase=%s outcome=%s execution=%s details=%v; want %+v", id,
			got.Phase, got.Outcome, got.Execution.State, got.DetailsAvailable, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func wantCode(t *testing.T, err error, code Code) {
	t.Helper()
	if err != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func newHarness(t *testing.T, ttl, accessLifetime time.Duration) *harness {
	t.Helper()
	f := &testClock{n: clock.Sample{Wall: time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC)}}
	c, err := New(f.now, ttl)
	must(t, err)
	h := &harness{c: c, f: f, access: "crd_" + strings.Repeat("a", 32), credential: "crd_" + strings.Repeat("b", 32)}
	if accessLifetime == 0 {
		accessLifetime = 24 * time.Hour
	}
	now := f.now().Wall
	accessSecret, mcpSecret := auth.Secret("rm_access_"), auth.Secret("rm_mcp_")
	must(t, c.InstallManagement(1, []CredentialRecord{
		{Credential: Credential{ID: h.access, Kind: "access", Name: "access", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(accessLifetime), CreationOperationID: strings.Repeat("a", 32)}, Digest: auth.Digest(accessSecret)},
		{Credential: Credential{ID: h.credential, Kind: "mcp", Name: "mcp", AllowExec: true, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), CreationOperationID: strings.Repeat("b", 32)}, Digest: auth.Digest(mcpSecret)},
	}))
	for _, item := range []struct{ secret, kind, id string }{{accessSecret, "access", h.access}, {mcpSecret, "mcp", h.credential}} {
		id, err := c.AuthenticateCredential(item.secret, item.kind)
		must(t, err)
		if id != item.id {
			t.Fatal("fixture credential identity differs")
		}
	}
	cl, err := c.BeginClient(h.credential, ClientInfo{Name: "test", Version: "1"})
	must(t, err)
	h.client = cl.ID
	_, err = c.ClientMessage(h.credential, h.client, true)
	must(t, err)
	s, err := c.Register(h.access)
	must(t, err)
	h.sid = s.ID
	var sample struct {
		Grant struct {
			Spec struct {
				Command, Reason, Shell string
				Timeout                int `json:"timeout_seconds"`
			}
		}
	}
	must(t, json.Unmarshal(fixture(t, "start.response"), &sample))
	x := sample.Grant.Spec
	h.spec = Spec{x.Command, x.Reason, x.Shell, x.Timeout}
	return h
}

func (h *harness) issueCredential(t *testing.T, kind string, allowExec bool, lifetime time.Duration) string {
	t.Helper()
	a, err := h.c.Login("")
	must(t, err)
	change, err := h.c.PrepareCredential(a.Token, a.CSRF, "create", "", CredentialInput{
		OperationID: auth.ID(""), Kind: kind, Name: "test", AllowExec: allowExec, ExpiresInSeconds: int64(lifetime / time.Second),
	})
	must(t, err)
	secret := auth.Secret("rm_" + kind + "_")
	v, _, err := h.c.CompleteCredential(change, auth.Digest(secret))
	must(t, err)
	id, err := h.c.AuthenticateCredential(secret, kind)
	must(t, err)
	if id != v.ID {
		t.Fatal("issued credential identity differs")
	}
	return id
}

func (h *harness) revokeCredential(id string) error {
	a, err := h.c.Login("")
	if err != nil {
		return err
	}
	change, err := h.c.PrepareCredential(a.Token, a.CSRF, "revoke", id, CredentialInput{})
	if err != nil {
		return err
	}
	_, _, err = h.c.CompleteCredential(change, [32]byte{})
	return err
}

func (h *harness) activate(t *testing.T, mode string) {
	t.Helper()
	_, err := h.c.Decide(h.sid, "accept", mode)
	must(t, err)
	h.poll(t, 1, nil)
}
func (h *harness) poll(t *testing.T, revision int64, acks []string) PollResult {
	t.Helper()
	p, err := h.c.BeginPoll(h.sid, revision, acks)
	must(t, err)
	v, err := h.c.CompletePoll(p)
	must(t, err)
	return v
}
func (h *harness) submit(t *testing.T, id int) Task {
	t.Helper()
	v, reused, err := h.c.Submit(h.credential, h.client, h.sid, fmt.Sprintf("%032x", id), h.spec)
	must(t, err)
	if reused {
		t.Fatal("unexpected reuse")
	}
	return v
}
func (h *harness) grant(t *testing.T, task Task) Grant {
	t.Helper()
	_, g, err := h.c.Start(h.sid, task.ID, 1)
	must(t, err)
	return g
}
func (h *harness) started(t *testing.T, task Task, g Grant) Task {
	t.Helper()
	v, err := h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "started", GrantID: g.ID})
	must(t, err)
	return v
}
func (h *harness) get(t *testing.T, task Task) Task {
	t.Helper()
	v, err := h.c.GetTask(h.credential, h.client, h.sid, task.ID, "")
	must(t, err)
	return v
}
func fullResult(g Grant, outcome string, exit *int) Result {
	r := Result{GrantID: g.ID, ConsentRevision: 1, Outcome: outcome, ExitCode: exit, Stdout: Output{Text: "ok\n", CapturedBytes: 3}}
	if exit == nil {
		r.ErrorCode = "EXIT_CODE_UNAVAILABLE"
	}
	return r
}

// D01/D02: the same accepted intent survives response loss and conflicting
// fields/credentials; current authority is checked before duplicate lookup.
func TestIntentIdempotenceAndAuthority(t *testing.T) {
	h := newHarness(t, 0, 0)
	h.activate(t, "full")
	first := h.submit(t, 1)
	for i := 0; i < 3; i++ {
		got, reused, err := h.c.Submit(h.credential, h.client, h.sid, first.RequestID, h.spec)
		must(t, err)
		if !reused || !reflect.DeepEqual(first, got) {
			t.Fatal("duplicate changed intent")
		}
	}
	for _, field := range []string{"command", "reason", "shell", "timeout"} {
		s := h.spec
		switch field {
		case "command":
			s.Command += " "
		case "reason":
			s.Reason += " "
		case "shell":
			s.Shell = "auto"
		case "timeout":
			s.TimeoutSeconds++
		}
		_, _, err := h.c.Submit(h.credential, h.client, h.sid, first.RequestID, s)
		wantCode(t, err, IdempotencyConflict)
	}
	other := h.issueCredential(t, "mcp", true, time.Hour)
	cl, err := h.c.BeginClient(other, ClientInfo{Name: "test", Version: "1"})
	must(t, err)
	_, err = h.c.ClientMessage(other, cl.ID, true)
	must(t, err)
	_, _, err = h.c.Submit(other, cl.ID, h.sid, first.RequestID, h.spec)
	wantCode(t, err, IdempotencyConflict)
	_, err = h.c.Reduce(h.sid, 1, "readonly")
	must(t, err)
	_, _, err = h.c.Submit(h.credential, h.client, h.sid, first.RequestID, h.spec)
	wantCode(t, err, ConsentRequired)
	got := h.get(t, first)
	if got.CredentialID != h.credential || got.ClientID != h.client {
		t.Fatal("initiator changed")
	}
}

// B02/G01: repeated delivery returns one immutable grant; losing it permits
// only an explicit not-started fact, never another start authorization.
func TestGrantRepeatExpiryAndLostResponse(t *testing.T) {
	t.Run("repeat", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		g := h.grant(t, task)
		h.f.step(4 * time.Second)
		_, again, err := h.c.Start(h.sid, task.ID, 1)
		must(t, err)
		if g != again {
			t.Fatal("grant or budgets changed")
		}
		h.started(t, task, g)
		_, _, err = h.c.Start(h.sid, task.ID, 1)
		wantCode(t, err, InvalidState)
		h.f.step(time.Second)
		_, _, err = h.c.Start(h.sid, task.ID, 1)
		wantCode(t, err, GrantExpired)
		if h.get(t, task).Phase != "running" {
			t.Fatal("duplicate expired start stopped running task")
		}
	})
	t.Run("response_lost", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		h.grant(t, task)
		got, err := h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "not_started", Reason: "GRANT_UNAVAILABLE"})
		must(t, err)
		expectedTask(t, got, "task.not_started")
		_, _, err = h.c.Start(h.sid, task.ID, 1)
		wantCode(t, err, InvalidState)
	})
	t.Run("unconfirmed_window", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		h.grant(t, task)
		h.f.step(5 * time.Second)
		got := h.get(t, task)
		if got.Phase != "stopping" || got.Stop.Reason != "start_unconfirmed" || got.Execution.State != "may_have_started" {
			t.Fatalf("%+v", got)
		}
	})
}

func TestResultAdjudicationAndDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name, input, stop, want string
		delay                   time.Duration
		code                    *int
	}{
		{"success", "succeeded", "", "succeeded", 0, ptr(0)},
		{"failure", "failed", "", "failed", 0, ptr(2)},
		{"missing_exit", "failed", "", "failed", 0, nil},
		{"local_cancel", "cancelled", "", "cancelled", 0, ptr(0)},
		{"local_timeout", "timed_out", "", "timed_out", 0, ptr(0)},
		{"operator_first", "timed_out", "operator", "cancelled", 0, ptr(0)},
		{"deadline_equal", "succeeded", "", "timed_out", 10 * time.Second, ptr(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 0, 0)
			h.activate(t, "full")
			task := h.submit(t, 1)
			g := h.grant(t, task)
			h.started(t, task, g)
			if tc.stop != "" {
				_, err := h.c.Cancel(h.credential, h.client, h.sid, task.ID)
				must(t, err)
			}
			h.f.step(tc.delay)
			r := fullResult(g, tc.input, tc.code)
			got, err := h.c.RecordResult(h.sid, task.ID, r)
			must(t, err)
			if got.Outcome != tc.want || got.Execution.State != "exited" {
				t.Fatalf("%+v", got)
			}
			if tc.stop == "" && tc.delay == 0 && got.Stop != nil {
				t.Fatal("invented server stop")
			}
			h.f.step(time.Millisecond)
			again, err := h.c.RecordResult(h.sid, task.ID, r)
			must(t, err)
			if !reflect.DeepEqual(got, again) {
				t.Fatal("duplicate changed settlement")
			}
			r.Stdout.Text = "different"
			_, err = h.c.RecordResult(h.sid, task.ID, r)
			wantCode(t, err, ResultConflict)
			_, err = h.c.Cancel(h.credential, h.client, h.sid, task.ID)
			must(t, err)
			if h.get(t, task).Outcome != tc.want {
				t.Fatal("late cancel changed outcome")
			}
		})
	}
}

func TestFactsOutOfOrder(t *testing.T) {
	t.Run("result_before_started", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		g := h.grant(t, task)
		v, err := h.c.RecordResult(h.sid, task.ID, fullResult(g, "succeeded", ptr(0)))
		must(t, err)
		expectedTask(t, v, "task.result_before_started")
		h.f.step(time.Second)
		v = h.started(t, task, g)
		expectedTask(t, v, "task.late_started")
		again, err := h.c.RecordResult(h.sid, task.ID, fullResult(g, "succeeded", ptr(0)))
		must(t, err)
		if !reflect.DeepEqual(v, again) {
			t.Fatal("duplicate result returned stale execution evidence")
		}
		if !v.Execution.StartObservedAt.After(*v.Execution.ExitObservedAt) {
			t.Fatal("lost receipt observations")
		}
		_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID, ExitCode: ptr(1)})
		wantCode(t, err, ResultConflict)
		_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID, ExitCode: ptr(0)})
		must(t, err)
		_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "not_started", Reason: "SPAWN_FAILED"})
		wantCode(t, err, ResultConflict)
		_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "stop_unconfirmed", GrantID: g.ID, ErrorCode: "PROCESS_STOP_UNCONFIRMED"})
		wantCode(t, err, ResultConflict)
	})
	t.Run("not_started_excludes_start_and_exit", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		g := h.grant(t, task)
		r := Receipt{Kind: "not_started", Reason: "SPAWN_FAILED"}
		_, err := h.c.RecordReceipt(h.sid, task.ID, r)
		must(t, err)
		_, err = h.c.RecordReceipt(h.sid, task.ID, r)
		must(t, err)
		r.GrantID = g.ID
		_, err = h.c.RecordReceipt(h.sid, task.ID, r)
		wantCode(t, err, ResultConflict)
		_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "started", GrantID: g.ID})
		wantCode(t, err, ResultConflict)
		_, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID})
		wantCode(t, err, ResultConflict)
		_, err = h.c.RecordResult(h.sid, task.ID, fullResult(g, "succeeded", ptr(0)))
		wantCode(t, err, ResultConflict)
	})
	t.Run("exit_without_result_keeps_session", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		g := h.grant(t, task)
		v, err := h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID})
		must(t, err)
		if v.Outcome != "unknown" || v.Execution.State != "exited" {
			t.Fatalf("%+v", v)
		}
		s, err := h.c.GetSession(h.sid)
		must(t, err)
		if s.State != "active" {
			t.Fatal("known exit ended session")
		}
		_, err = h.c.RecordResult(h.sid, task.ID, fullResult(g, "succeeded", ptr(0)))
		wantCode(t, err, ResultConflict)
		h.submit(t, 2) // known exit does not occupy the only execution slot
	})
}

func TestRevocationUnknownAndLateEvidence(t *testing.T) {
	t.Run("V01_before_grant", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		must(t, h.revokeCredential(h.access))
		_, _, err := h.c.Start(h.sid, task.ID, 1)
		wantCode(t, err, SessionEnded)
		expectedTask(t, h.get(t, task), "task.revoked_before_grant")
		s, err := h.c.GetSession(h.sid)
		must(t, err)
		if s.Cleanup != "not_needed" || s.EndReason != "access_invalid" {
			t.Fatalf("%+v", s)
		}
	})
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprintf("late_not_started_%v", started), func(t *testing.T) {
			h := newHarness(t, 0, 0)
			h.activate(t, "full")
			task := h.submit(t, 1)
			g := h.grant(t, task)
			if started {
				h.started(t, task, g)
			}
			_, err := h.c.Cancel(h.credential, h.client, h.sid, task.ID)
			must(t, err)
			h.f.step(5 * time.Second)
			unknown := h.get(t, task)
			if unknown.Outcome != "unknown" || unknown.Execution.State != "unknown" {
				t.Fatalf("%+v", unknown)
			}
			r := Receipt{Kind: "not_started", Reason: "CANCELLED_BEFORE_SPAWN"}
			got, err := h.c.RecordReceipt(h.sid, task.ID, r)
			if started {
				wantCode(t, err, ResultConflict)
				got, err = h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID})
				must(t, err)
			} else {
				must(t, err)
				expectedTask(t, got, "task.unknown.late_not_started")
			}
			if got.Outcome != "unknown" || !got.SettledAt.Equal(*unknown.SettledAt) {
				t.Fatal("late fact rewrote outcome")
			}
			s, err := h.c.GetSession(h.sid)
			must(t, err)
			if s.Cleanup != "confirmed" {
				t.Fatalf("%+v", s)
			}
		})
	}
	t.Run("V03_cleanup_after_access_revoked", func(t *testing.T) {
		h := newHarness(t, 0, 0)
		h.activate(t, "full")
		task := h.submit(t, 1)
		g := h.grant(t, task)
		h.started(t, task, g)
		must(t, h.revokeCredential(h.access))
		h.f.step(5 * time.Second)
		v := h.get(t, task)
		expectedTask(t, v, "task.race.unknown")
		h.f.step(time.Second)
		_, err := h.c.RecordResult(h.sid, task.ID, fullResult(g, "succeeded", ptr(0)))
		wantCode(t, err, SessionEnded)
		late, err := h.c.RecordReceipt(h.sid, task.ID, Receipt{Kind: "exited", GrantID: g.ID, ExitCode: ptr(0)})
		must(t, err)
		expectedTask(t, late, "task.race.late_exit")
		s, err := h.c.GetSession(h.sid)
		must(t, err)
		if s.EndReason != "access_invalid" || s.Cleanup != "confirmed" {
			t.Fatalf("%+v", s)
		}
	})
}
