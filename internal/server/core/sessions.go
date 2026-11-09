package core

import (
	"sort"
	"time"

	"remota.local/remota/internal/clock"
)

func (c *Core) Register(accessID string) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	v, err := c.credential(accessID, "access")
	if err != nil {
		return Session{}, err
	}
	if err = c.writable(); err != nil {
		return Session{}, err
	}
	active := 0
	for _, s := range c.sessions {
		if s.State != "ended" {
			active++
		}
	}
	if active >= 256 || len(c.sessions) >= 512 {
		return Session{}, CapacityExceeded
	}
	expires := earlier(after(n, c.ttl), v.expires)
	decision := earlier(after(n, decisionWait), expires)
	s := &sessionState{Session: Session{ID: newID("ses"), State: "pending", CreatedAt: n.Wall,
		ExpiresAt: expires.wall, DecisionDeadline: decision.wall}, accessID: accessID,
		expires: expires, decision: decision, tasks: make(map[string]*taskState), requests: make(map[string]string)}
	c.sessions[s.ID] = s
	return sessionView(s, n), nil
}

func (c *Core) Decide(sid, decision, mode string) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	s, err := c.activeSession(sid)
	if err != nil {
		return Session{}, err
	}
	if (decision != "accept" || (mode != "full" && mode != "readonly")) &&
		(decision != "reject" || mode != "") {
		return Session{}, InvalidArgument
	}
	if s.State == "active" {
		if decision != "accept" || mode != s.initialMode {
			return Session{}, InvalidState
		}
		return sessionView(s, n), nil
	}
	if decision == "reject" {
		c.endSession(s, "local_rejected", "session_ended", n)
	} else {
		s.State, s.initialMode = "active", mode
		s.Consent = &Consent{Mode: mode, Revision: 1, DecidedAt: n.Wall}
		s.activation = earlier(after(n, activationWait), s.expires)
	}
	return sessionView(s, n), nil
}

func (c *Core) Reduce(sid string, expectedRevision int64, mode string) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	s, err := c.activeSession(sid)
	if err != nil {
		return Session{}, err
	}
	if mode == "full" {
		return Session{}, Reconsent
	}
	if mode != "readonly" || expectedRevision < 1 {
		return Session{}, InvalidArgument
	}
	if s.State != "active" {
		return Session{}, InvalidState
	}
	if s.Consent.Mode == "readonly" {
		return sessionView(s, n), nil
	}
	if s.Consent.Revision != expectedRevision {
		return Session{}, RevisionConflict
	}
	s.Consent.Mode = "readonly"
	s.Consent.Revision++
	for _, t := range s.tasks {
		c.stopTask(t, "local_revoked", n)
	}
	return sessionView(s, n), nil
}

// End is an internal close boundary. Agent/admin adapters restrict which reasons
// they may supply; neither can grant full or extend a session.
func (c *Core) End(sid, reason string) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	s := c.sessions[sid]
	if s == nil {
		return Session{}, NotFound
	}
	switch reason {
	case "local_end", "stdin_eof", "signal", "lease_expired", "protocol_error", "admin_end":
	default:
		return Session{}, InvalidArgument
	}
	c.endSession(s, reason, "session_ended", n)
	return sessionView(s, n), nil
}

func (c *Core) activeSession(id string) (*sessionState, error) {
	s := c.sessions[id]
	if s == nil {
		return nil, NotFound
	}
	if s.State == "ended" {
		return nil, SessionEnded
	}
	return s, nil
}

func (c *Core) endSession(s *sessionState, reason, stopReason string, n clock.Sample) {
	if s.State == "ended" {
		return
	}
	s.State, s.EndReason, s.EndedAt = "ended", reason, ptr(n.Wall)
	s.retain = after(n, retention)
	s.endEvent = &Event{ID: newID("evt"), Type: "session.end", SessionID: s.ID,
		CreatedAt: n.Wall, Reason: reason, EndedAt: ptr(n.Wall)}
	for _, t := range s.tasks {
		c.stopTask(t, stopReason, n)
		// Remove all copies of command/output, including duplicate and event caches.
		t.Spec, t.Result, t.result, t.offerEvent = nil, nil, nil, nil
		t.DetailsAvailable = false
		if t.grant != nil {
			t.grant.Spec = Spec{}
		}
	}
}

func sessionView(s *sessionState, n clock.Sample) Session {
	v := s.Session
	v.Consent, v.EndedAt = copyPtr(v.Consent), copyPtr(v.EndedAt)
	v.Capabilities = []string{}
	if s.State != "ended" {
		v.RemainingMS = s.expires.remaining(n)
	}
	if s.State == "pending" {
		v.DecisionRemainingMS = s.decision.remaining(n)
	}
	if s.State == "active" && s.leaseSeq == 0 {
		v.ActivationRemainingMS = s.activation.remaining(n)
	}
	v.Activity, v.LastSeenAt = "never_seen", nil
	if s.lastSeen != nil {
		v.LastSeenAt = ptr(s.lastSeen.Wall)
		v.Activity = "recent"
		if max(n.Wall.Sub(s.lastSeen.Wall), n.Elapsed-s.lastSeen.Elapsed) > 15*time.Second {
			v.Activity = "stale"
		}
	}
	if s.State == "active" {
		v.Capabilities = []string{"ping", "info"}
		if s.Consent.Mode == "full" {
			v.Capabilities = append(v.Capabilities, "exec")
		}
	}
	if s.State == "ended" {
		v.Cleanup = "not_needed"
		pending, unknown, granted := false, false, false
		for _, t := range s.tasks {
			if t.grant == nil {
				continue
			}
			granted = true
			switch t.Execution.State {
			case "unknown":
				unknown = true
			case "exited", "not_started":
			default:
				pending = true
			}
		}
		if granted {
			v.Cleanup = "confirmed"
		}
		if pending {
			v.Cleanup = "pending"
		}
		if unknown {
			v.Cleanup = "unknown"
		}
	}
	return v
}

func (c *Core) GetSession(sid string) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	s := c.sessions[sid]
	if s == nil {
		return Session{}, NotFound
	}
	return sessionView(s, n), nil
}

func (c *Core) BeginClient(credentialID string) (Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	v, err := c.credential(credentialID, "mcp")
	if err != nil {
		return Client{}, err
	}
	if err = c.writable(); err != nil {
		return Client{}, err
	}
	count := 0
	for _, cl := range c.clients {
		if cl.Lifecycle != "ended" {
			count++
		}
	}
	if count >= 256 {
		return Client{}, CapacityExceeded
	}
	d := earlier(after(n, initWait), v.expires)
	cl := &clientState{Client: Client{ID: newID("mcl"), CredentialID: credentialID, Lifecycle: "initializing",
		CreatedAt: n.Wall}, expires: d, lastSeen: n}
	c.clients[cl.ID] = cl
	return clientView(cl, n), nil
}

// ClientMessage is called for each authenticated, bound MCP message. initialized
// is true only for notifications/initialized; other messages need a ready client.
func (c *Core) ClientMessage(credentialID, clientID string, initialized bool) (Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	v, err := c.credential(credentialID, "mcp")
	if err != nil {
		return Client{}, err
	}
	cl := c.clients[clientID]
	if cl == nil || cl.CredentialID != credentialID || cl.Lifecycle == "ended" {
		return Client{}, ClientNotFound
	}
	if cl.Lifecycle == "initializing" && !initialized {
		return Client{}, InvalidState
	}
	if err = c.writable(); err != nil {
		return Client{}, err
	}
	cl.Lifecycle, cl.lastSeen = "ready", n
	cl.expires = earlier(after(n, idleWait), v.expires)
	return clientView(cl, n), nil
}

func (c *Core) client(credentialID, clientID string) (*credentialState, error) {
	v, err := c.credential(credentialID, "mcp")
	if err != nil {
		return nil, err
	}
	cl := c.clients[clientID]
	if cl == nil || cl.CredentialID != credentialID || cl.Lifecycle == "ended" {
		return nil, ClientNotFound
	}
	if cl.Lifecycle != "ready" {
		return nil, InvalidState
	}
	return v, nil
}

func (c *Core) EndClient(id, reason string) (Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	cl := c.clients[id]
	if cl == nil {
		return Client{}, ClientNotFound
	}
	if reason != "client_delete" && reason != "admin_end" {
		return Client{}, InvalidArgument
	}
	c.endClient(cl, reason, "mcp_client_ended", n)
	return clientView(cl, n), nil
}

func (c *Core) endClient(cl *clientState, reason, stopReason string, n clock.Sample) {
	if cl.Lifecycle == "ended" {
		return
	}
	cl.Lifecycle, cl.EndReason, cl.EndedAt = "ended", reason, ptr(n.Wall)
	cl.retain = after(n, retention)
	for _, s := range c.sessions {
		for _, t := range s.tasks {
			if t.ClientID == cl.ID {
				c.stopTask(t, stopReason, n)
			}
		}
	}
}

func clientView(cl *clientState, n clock.Sample) Client {
	v := cl.Client
	v.LastSeenAt, v.ExpiresAt = cl.lastSeen.Wall, cl.expires.wall
	v.EndedAt = copyPtr(v.EndedAt)
	v.Activity = "recent"
	if max(n.Wall.Sub(cl.lastSeen.Wall), n.Elapsed-cl.lastSeen.Elapsed) > time.Minute {
		v.Activity = "idle"
	}
	if cl.Lifecycle == "ended" {
		v.Activity = "expired"
	}
	return v
}

func (c *Core) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	v := Snapshot{Status: c.status, Epoch: c.epoch, Sessions: []Session{}, Clients: []Client{}, Tasks: []Task{}}
	for _, s := range c.sessions {
		v.Sessions = append(v.Sessions, sessionView(s, n))
		for _, task := range s.tasks {
			v.Tasks = append(v.Tasks, taskView(task, false))
		}
	}
	for _, client := range c.clients {
		v.Clients = append(v.Clients, clientView(client, n))
	}
	sort.Slice(v.Sessions, func(i, j int) bool {
		return before(v.Sessions[i].CreatedAt, v.Sessions[i].ID, v.Sessions[j].CreatedAt, v.Sessions[j].ID)
	})
	sort.Slice(v.Clients, func(i, j int) bool {
		return before(v.Clients[i].CreatedAt, v.Clients[i].ID, v.Clients[j].CreatedAt, v.Clients[j].ID)
	})
	sort.Slice(v.Tasks, func(i, j int) bool {
		return before(v.Tasks[i].CreatedAt, v.Tasks[i].ID, v.Tasks[j].CreatedAt, v.Tasks[j].ID)
	})
	return v
}

func before(a time.Time, aid string, b time.Time, bid string) bool {
	return a.Before(b) || (a.Equal(b) && aid < bid)
}
