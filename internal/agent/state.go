package agent

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"remota.local/remota/internal/clock"
	"remota.local/remota/internal/protocol"
)

var errStale = errors.New("stale or cancelled response")

type sessionView struct {
	id, state, mode, operator  string
	revision                   int64
	created, expires, decision time.Time
}

// readSession receives a response already validated by the transport.
func readSession(value any) (sessionView, error) {
	m := value.(map[string]any)
	v := sessionView{id: m["id"].(string), state: m["state"].(string), operator: m["operator_display_name"].(string)}
	if !utf8.ValidString(v.operator) || len(v.operator) > 80 {
		return v, ErrProtocol
	}
	var err error
	if v.created, err = time.Parse(time.RFC3339Nano, m["created_at"].(string)); err != nil {
		return v, ErrProtocol
	}
	if v.expires, err = time.Parse(time.RFC3339Nano, m["expires_at"].(string)); err != nil {
		return v, ErrProtocol
	}
	if v.decision, err = time.Parse(time.RFC3339Nano, m["decision_deadline"].(string)); err != nil {
		return v, ErrProtocol
	}
	if !v.expires.After(v.created) || v.expires.Sub(v.created) > 8*time.Hour || v.decision.After(v.expires) || v.decision.Before(v.created) {
		return v, ErrProtocol
	}
	if consent, ok := m["consent"].(map[string]any); ok {
		v.mode, v.revision = consent["mode"].(string), protocol.Integer(consent["revision"])
	}
	return v, nil
}

type localState struct {
	mu                        sync.Mutex
	clock                     clock.Source
	last                      time.Duration
	phase, reason             string
	session                   sessionView
	sessionEnd, decisionEnd   time.Duration
	activationEnd, leaseEnd   time.Duration
	serverIssued              time.Time
	seq, revision, generation int64
	reducing                  bool
	done                      chan struct{}
	cancel                    func()
}

func newLocalState(source clock.Source, cancel func()) *localState {
	return &localState{clock: source, phase: "new", done: make(chan struct{}), cancel: cancel}
}

func (s *localState) endLocked(reason string) {
	if s.phase == "ended" {
		return
	}
	s.phase, s.reason = "ended", reason
	s.generation++
	close(s.done)
	s.cancel()
}

func (s *localState) end(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endLocked(reason)
}

func (s *localState) sampleLocked() (clock.Sample, bool) {
	n := s.clock()
	if n.Wall.IsZero() || n.Elapsed < s.last {
		s.endLocked("protocol_error")
		return n, false
	}
	s.last = n.Elapsed
	if s.phase == "ended" {
		return n, false
	}
	if s.phase != "new" && n.Elapsed >= s.sessionEnd ||
		s.phase == "awaiting_decision" && n.Elapsed >= s.decisionEnd ||
		(s.phase == "confirming" || s.phase == "reconnecting" && s.seq == 0) && n.Elapsed >= s.activationEnd ||
		(s.phase == "active" || s.phase == "reconnecting" && s.seq > 0) && n.Elapsed >= s.leaseEnd {
		s.endLocked("lease_expired")
		return n, false
	}
	return n, true
}

func (s *localState) sample() (clock.Sample, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sampleLocked()
}

func budget(sent time.Duration, millis int64) (time.Duration, bool) {
	if millis <= 0 || millis > int64((8*time.Hour)/time.Millisecond) {
		return 0, false
	}
	d := time.Duration(millis) * time.Millisecond
	if sent > time.Duration(1<<63-1)-d {
		return 0, false
	}
	return sent + d, true
}

func (s *localState) register(sent clock.Sample, m map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := readSession(m["session"])
	if err != nil || v.state != "pending" || v.mode != "" {
		return ErrProtocol
	}
	remaining, decision := protocol.Integer(m["session_remaining_ms"]), protocol.Integer(m["decision_remaining_ms"])
	end, ok := budget(sent.Elapsed, remaining)
	if !ok || remaining > v.expires.Sub(v.created).Milliseconds() || decision > remaining ||
		decision > v.decision.Sub(v.created).Milliseconds() {
		return ErrProtocol
	}
	decisionEnd, ok := budget(sent.Elapsed, decision)
	if !ok {
		return ErrProtocol
	}
	s.session, s.phase = v, "awaiting_decision"
	s.sessionEnd, s.decisionEnd, s.last = end, decisionEnd, sent.Elapsed
	if _, ok = s.sampleLocked(); !ok {
		return ErrProtocol
	}
	return nil
}

func (s *localState) decision(sent clock.Sample, m map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sampleLocked(); !ok || s.phase != "awaiting_decision" {
		return ErrProtocol
	}
	v, err := readSession(m["session"])
	if err != nil || !sameSession(s.session, v) || v.state != "active" || v.mode != "readonly" || v.revision != 1 {
		return ErrProtocol
	}
	value, ok := m["activation_remaining_ms"]
	if !ok {
		return ErrProtocol
	}
	ms := protocol.Integer(value)
	activation, ok := budget(sent.Elapsed, ms)
	if !ok {
		return ErrProtocol
	}
	s.phase, s.revision = "confirming", v.revision
	s.activationEnd = min(activation, s.sessionEnd)
	if _, ok = s.sampleLocked(); !ok {
		return ErrProtocol
	}
	return nil
}

func sameSession(a, b sessionView) bool {
	return a.id == b.id && a.operator == b.operator && a.created.Equal(b.created) && a.expires.Equal(b.expires) && a.decision.Equal(b.decision)
}

func (s *localState) pollStart() (clock.Sample, int64, int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.sampleLocked()
	if !ok || s.phase != "confirming" && s.phase != "active" && s.phase != "reconnecting" {
		return n, 0, 0, false
	}
	return n, s.revision, s.generation, true
}

func (s *localState) pollFailed(generation int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sampleLocked(); ok && s.generation == generation {
		s.phase = "reconnecting"
		s.generation++
	}
}

func (s *localState) acceptPoll(ctx context.Context, sent clock.Sample, generation int64, m map[string]any) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sampleLocked(); !ok || generation != s.generation || ctx.Err() != nil {
		return time.Time{}, errStale
	}
	v, err := readSession(m["session"])
	if err != nil || !sameSession(s.session, v) {
		return time.Time{}, ErrProtocol
	}
	if v.state == "ended" {
		s.endLocked("")
		return time.Time{}, nil
	}
	if v.state != "active" || v.mode != "readonly" || v.revision != s.revision {
		return time.Time{}, ErrProtocol
	}
	lease, ok := m["lease"].(map[string]any)
	if !ok {
		return time.Time{}, ErrProtocol
	}
	seq, rev := protocol.Integer(lease["seq"]), protocol.Integer(lease["consent_revision"])
	ms, remaining := protocol.Integer(lease["valid_for_ms"]), protocol.Integer(m["session_remaining_ms"])
	issued, err := time.Parse(time.RFC3339Nano, lease["issued_at"].(string))
	if err != nil {
		return time.Time{}, ErrProtocol
	}
	expires, err := time.Parse(time.RFC3339Nano, lease["expires_at"].(string))
	if err != nil || issued.Before(v.created) || !s.serverIssued.IsZero() && issued.Before(s.serverIssued) || !expires.After(issued) || expires.After(v.expires) || expires.Sub(issued) > 30*time.Second || expires.Sub(issued) < time.Duration(ms)*time.Millisecond ||
		seq <= s.seq || rev != s.revision || ms > 30000 || ms > remaining {
		return time.Time{}, ErrProtocol
	}
	leaseEnd, ok := budget(sent.Elapsed, ms)
	if !ok {
		return time.Time{}, ErrProtocol
	}
	sessionEnd, ok := budget(sent.Elapsed, remaining)
	if !ok {
		return time.Time{}, ErrProtocol
	}
	s.sessionEnd = min(s.sessionEnd, sessionEnd)
	s.leaseEnd, s.seq, s.phase, s.serverIssued = min(leaseEnd, s.sessionEnd), seq, "active", issued
	if _, ok = s.sampleLocked(); !ok {
		return time.Time{}, ErrProtocol
	}
	return issued, nil
}

func (s *localState) permit(revision int64, deadline time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.sampleLocked()
	return ok && s.phase == "active" && revision == s.revision && n.Elapsed < deadline
}
