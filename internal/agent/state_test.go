package agent

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"remota.local/remota/internal/clock"
	"remota.local/remota/internal/protocol"
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

func fixture(t *testing.T, id string) map[string]any {
	t.Helper()
	b, err := os.ReadFile("../../docs/contracts/acceptance/messages.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Valid []struct {
			ID    string
			Value json.RawMessage
		}
	}
	if err = json.Unmarshal(b, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, item := range corpus.Valid {
		if item.ID == id {
			v, err := protocol.Parse(item.Value)
			if err != nil {
				t.Fatal(err)
			}
			return v.(map[string]any)
		}
	}
	t.Fatalf("fixture %s not found", id)
	return nil
}

func activeState(t *testing.T) (*localState, *testClock, clock.Sample, int64, map[string]any) {
	t.Helper()
	f := &testClock{n: clock.Sample{Wall: time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC)}}
	s := newLocalState(f.now, func() {})
	if err := s.register(f.now(), fixture(t, "registration")); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Second, time.Second)
	decision := fixture(t, "decision.response")
	readonly := fixture(t, "poll.readonly")
	decision["session"] = readonly["session"]
	if err := s.decision(f.now(), decision, "readonly"); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Second, time.Second)
	sent, revision, generation, ok := s.pollStart()
	if !ok || revision != 1 {
		t.Fatal("first poll not allowed")
	}
	if _, err := s.acceptPoll(context.Background(), sent, generation, readonly); err != nil {
		t.Fatal(err)
	}
	return s, f, sent, generation, readonly
}

func TestLeaseUsesSendTimeAndRejectsLateResponse(t *testing.T) {
	s, f, _, generation, poll := activeState(t)
	f.advance(-time.Hour, 10*time.Second)
	sent, revision, _, ok := s.pollStart()
	if !ok || revision != 1 {
		t.Fatal("wall clock rollback ended a valid lease")
	}
	poll["lease"].(map[string]any)["seq"] = json.Number("2")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.acceptPoll(ctx, sent, generation, poll); err != errStale {
		t.Fatalf("cancelled poll accepted: %v", err)
	}
	s.pollFailed(generation)
	f.advance(0, 20*time.Second)
	if _, ok = s.sample(); ok {
		t.Fatal("lease survived its send-time deadline")
	}
	if _, err := s.acceptPoll(context.Background(), sent, generation, poll); err != errStale {
		t.Fatalf("late response revived session: %v", err)
	}
}

func TestWatchdogExpiresDuringSuspendAndClockFailure(t *testing.T) {
	for _, regression := range []bool{false, true} {
		t.Run(map[bool]string{false: "suspend", true: "clock_failure"}[regression], func(t *testing.T) {
			s, f, _, _, _ := activeState(t)
			go s.watch()
			if regression {
				f.advance(0, -time.Second)
			} else {
				f.advance(0, 30*time.Second)
			}
			select {
			case <-s.done:
			case <-time.After(time.Second):
				t.Fatal("independent watchdog did not end authorization")
			}
		})
	}
}

func TestReconnectingPausesDiagnosticsUntilFreshLease(t *testing.T) {
	s, f, _, generation, poll := activeState(t)
	if !s.permit(1, s.leaseEnd) {
		t.Fatal("valid lease did not allow diagnostics")
	}
	s.pollFailed(generation)
	if s.permit(1, s.leaseEnd) {
		t.Fatal("diagnostics remained enabled during reconnect")
	}
	f.advance(time.Second, time.Second)
	sent, _, nextGeneration, ok := s.pollStart()
	if !ok || nextGeneration == generation {
		t.Fatal("reconnect did not use a new request generation")
	}
	lease := poll["lease"].(map[string]any)
	lease["seq"] = json.Number("2")
	lease["issued_at"] = "2026-11-08T04:00:03.000Z"
	lease["expires_at"] = "2026-11-08T04:00:33.000Z"
	poll["session_remaining_ms"] = json.Number("3597000")
	if _, err := s.acceptPoll(context.Background(), sent, nextGeneration, poll); err != nil {
		t.Fatal(err)
	}
	if !s.permit(1, s.leaseEnd) {
		t.Fatal("fresh lease did not resume diagnostics")
	}
	s.end("local_end")
	if _, err := s.acceptPoll(context.Background(), sent, nextGeneration, poll); err != errStale {
		t.Fatalf("late poll revived locally ended session: %v", err)
	}
}

func TestLateRegistrationCannotGrantConsent(t *testing.T) {
	f := &testClock{n: clock.Sample{Wall: time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC)}}
	sent := f.now()
	f.advance(2*time.Minute, 2*time.Minute)
	s := newLocalState(f.now, func() {})
	if err := s.register(sent, fixture(t, "registration")); err == nil {
		t.Fatal("expired registration granted a decision window")
	}
}
