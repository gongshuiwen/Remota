package core

import "sort"

// Poll is an opaque in-flight handle. Adapters wait outside the core, then call
// CompletePoll or AbortPoll exactly once. Only completion can renew the lease.
type Poll struct {
	sessionID string
	revision  int64
}

func (c *Core) BeginPoll(sid string, revision int64, acknowledgements []string) (*Poll, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	s := c.sessions[sid]
	if s == nil {
		return nil, NotFound
	}
	if err := c.pollGate(s, revision); err != nil {
		return nil, err
	}
	if len(acknowledgements) > 16 {
		return nil, InvalidArgument
	}
	seen := make(map[string]bool, len(acknowledgements))
	for _, id := range acknowledgements {
		if !validID(id, "evt") || seen[id] {
			return nil, InvalidArgument
		}
		seen[id] = true
	}
	if s.poll != nil {
		return nil, AlreadyPolling
	}
	for id := range seen {
		if s.endEvent != nil && id == s.endEvent.ID {
			s.endAck = true
		}
		for _, t := range s.tasks {
			if t.offerEvent != nil && id == t.offerEvent.ID {
				t.offerAck = true
			}
		}
	}
	p := &Poll{sessionID: sid, revision: revision}
	s.poll = p
	return p, nil
}

func (c *Core) pollGate(s *sessionState, revision int64) error {
	if revision < 0 || revision > 9007199254740991 {
		return InvalidArgument
	}
	if s.State == "ended" {
		return nil
	}
	if s.State != "active" {
		return InvalidState
	}
	if revision != s.Consent.Revision {
		return RevisionConflict
	}
	return c.writable()
}

func (c *Core) AbortPoll(p *Poll) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	if p != nil {
		if s := c.sessions[p.sessionID]; s != nil && s.poll == p {
			s.poll = nil
		}
	}
}

func (c *Core) CompletePoll(p *Poll) (PollResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if p == nil {
		return PollResult{}, InvalidArgument
	}
	s := c.sessions[p.sessionID]
	if s == nil {
		return PollResult{}, NotFound
	}
	if s.poll != p {
		return PollResult{}, InvalidState
	}
	s.poll = nil
	if err := c.pollGate(s, p.revision); err != nil {
		return PollResult{}, err
	}
	v := PollResult{Events: []Event{}}
	if s.State == "active" {
		s.lease = earlier(after(n, leaseWindow), s.expires)
		s.leaseSeq++
		s.lastSeen = ptr(n)
		v.SessionRemainingMS = s.expires.remaining(n)
		v.Lease = &Lease{Seq: s.leaseSeq, ConsentRevision: s.Consent.Revision,
			IssuedAt: n.Wall, ExpiresAt: s.lease.wall, ValidForMS: s.lease.remaining(n)}
	}
	v.Session = sessionView(s, n)
	if s.endEvent != nil && !s.endAck {
		v.Events = append(v.Events, cloneEvent(*s.endEvent))
	}
	for _, t := range s.tasks {
		if t.cancelEvent != nil && t.Execution.State != "exited" && t.Execution.State != "not_started" {
			v.Events = append(v.Events, cloneEvent(*t.cancelEvent))
		}
		if s.State == "active" && s.Consent.Mode == "full" && t.Phase == "queued" && !t.offerAck {
			v.Events = append(v.Events, cloneEvent(*t.offerEvent))
		}
	}
	priority := func(kind string) int {
		switch kind {
		case "session.end":
			return 0
		case "task.cancel":
			return 1
		default:
			return 2
		}
	}
	sort.Slice(v.Events, func(i, j int) bool {
		a, b := v.Events[i], v.Events[j]
		if priority(a.Type) != priority(b.Type) {
			return priority(a.Type) < priority(b.Type)
		}
		return before(a.CreatedAt, a.ID, b.CreatedAt, b.ID)
	})
	if len(v.Events) > 16 {
		v.Events = v.Events[:16]
	}
	return v, nil
}

func cloneEvent(e Event) Event {
	e.Deadline, e.EndedAt, e.ConfirmationDeadline = copyPtr(e.Deadline), copyPtr(e.EndedAt), copyPtr(e.ConfirmationDeadline)
	if e.Task != nil {
		t := cloneTask(*e.Task)
		e.Task = &t
	}
	return e
}
