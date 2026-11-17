package core

import (
	"crypto/subtle"
	"strings"
	"time"
	"unicode/utf8"

	"remota.local/remota/internal/auth"
	"remota.local/remota/internal/clock"
)

type adminSession struct {
	csrf    string
	expires deadline
}
type AdminLogin struct {
	Token, CSRF string
	ExpiresAt   time.Time
}
type CredentialInput struct {
	OperationID, Kind, Name string
	ExpiresInSeconds        int64
	AllowExec               bool
}

// CredentialChange is an opaque pending effect. The storage coordinator writes
// its metadata outside the core lock, then confirms or fails the change.
type CredentialChange struct {
	Credential *Credential
	Previous   *Credential
	Revision   int64
	Effects    Effects
	Noop       bool
	owner      [32]byte
	accepted   clock.Sample
}

func validText(s string, lo, hi int) bool { return utf8.ValidString(s) && len(s) >= lo && len(s) <= hi }
func NormalizeCredentialName(s string) (string, error) {
	s = strings.TrimFunc(s, whiteSpace)
	if !validText(s, 1, 80) {
		return "", InvalidArgument
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return "", InvalidArgument
		}
	}
	return s, nil
}

func (c *Core) Epoch() string { return c.epoch }

func (c *Core) Status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	return c.status
}

// InstallManagement runs only during startup, after durable-state validation.
func (c *Core) InstallManagement(revision int64, records []CredentialRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if len(c.credentials) != 0 || revision < 1 {
		return InvalidState
	}
	for _, r := range records {
		v := r.Credential
		if !ValidID(v.ID, "crd") || !ValidID(v.CreationOperationID, "") {
			return InvalidArgument
		}
		if _, ok := c.credentials[v.ID]; ok {
			return InvalidArgument
		}
		if _, ok := c.operations[v.CreationOperationID]; ok {
			return InvalidArgument
		}
		c.credentials[v.ID] = &credentialState{Credential: v, expires: after(n, v.ExpiresAt.Sub(n.Wall)), expired: !n.Wall.Before(v.ExpiresAt)}
		c.digests[r.Digest] = v.ID
		c.operations[v.CreationOperationID] = v.ID
	}
	c.managementRevision = revision
	return nil
}

func (c *Core) AuthenticateCredential(secret, kind string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	if (kind != "access" && kind != "mcp") || !auth.ValidSecret(secret, kind) {
		return "", Unauthenticated
	}
	id := c.digests[auth.Digest(secret)]
	if _, err := c.credential(id, kind); err != nil {
		return "", err
	}
	if err := c.writable(); err != nil {
		return "", err
	}
	return id, nil
}

func (c *Core) Login(replace string) (AdminLogin, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if err := c.writable(); err != nil {
		return AdminLogin{}, err
	}
	old := auth.Digest(replace)
	_, hasOld := c.admins[old]
	if len(c.admins) >= 8 && !hasOld {
		return AdminLogin{}, CapacityExceeded
	}
	token, csrf := auth.Secret(""), auth.Secret("")
	d := after(n, 8*time.Hour)
	delete(c.admins, old)
	c.admins[auth.Digest(token)] = &adminSession{csrf: csrf, expires: d}
	return AdminLogin{token, csrf, d.wall}, nil
}

func (c *Core) admin(token, csrf string, mutation bool) (*adminSession, error) {
	a := c.admins[auth.Digest(token)]
	if a == nil {
		return nil, Unauthenticated
	}
	if mutation && subtle.ConstantTimeCompare([]byte(csrf), []byte(a.csrf)) != 1 {
		return nil, CSRFInvalid
	}
	return a, nil
}

func (c *Core) CheckAdmin(token, csrf string, mutation bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	_, err := c.admin(token, csrf, mutation)
	return err
}

func (c *Core) Logout(token, csrf string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	if _, err := c.admin(token, csrf, true); err != nil {
		return err
	}
	delete(c.admins, auth.Digest(token))
	return nil
}

func (c *Core) AdminState(token string) (Snapshot, AdminLogin, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	a, err := c.admin(token, "", false)
	if err != nil {
		return Snapshot{}, AdminLogin{}, err
	}
	return c.snapshot(c.last, false), AdminLogin{CSRF: a.csrf, ExpiresAt: a.expires.wall}, nil
}

func (c *Core) credentialView(v *credentialState, counts Counts) Credential {
	r := v.Credential
	r.RevokedAt = copyPtr(r.RevokedAt)
	r.Status = "active"
	if v.expired {
		r.Status = "expired"
	}
	if v.Revoked {
		r.Status = "revoked"
	}
	if r.Status == "active" {
		r.RemainingMS = v.expires.remaining(c.last)
	}
	r.EffectsCount = counts
	return r
}

func (c *Core) countsByCredential() map[string]Counts {
	counts := make(map[string]Counts)
	for _, s := range c.sessions {
		if s.State != "ended" {
			v := counts[s.accessID]
			v.LiveSessions++
			counts[s.accessID] = v
		}
		for _, t := range s.tasks {
			if t.Phase == "settled" {
				continue
			}
			v := counts[s.accessID]
			v.UnsettledTasks++
			counts[s.accessID] = v
			if t.CredentialID != s.accessID {
				v = counts[t.CredentialID]
				v.UnsettledTasks++
				counts[t.CredentialID] = v
			}
		}
	}
	for _, cl := range c.clients {
		if cl.Lifecycle != "ended" {
			v := counts[cl.CredentialID]
			v.LiveClients++
			counts[cl.CredentialID] = v
		}
	}
	return counts
}

func (c *Core) effects(id string) Effects {
	var e Effects
	for _, s := range c.sessions {
		if s.accessID == id && s.State != "ended" {
			e.SessionsEnded++
		}
		for _, t := range s.tasks {
			if t.Phase != "settled" && t.Stop == nil && (s.accessID == id || t.CredentialID == id) {
				e.TasksStopRequested++
			}
		}
	}
	for _, cl := range c.clients {
		if cl.CredentialID == id && cl.Lifecycle != "ended" {
			e.ClientsEnded++
		}
	}
	return e
}

func (c *Core) PrepareCredential(token, csrf, action, target string, in CredentialInput) (*CredentialChange, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if _, err := c.admin(token, csrf, true); err != nil {
		return nil, err
	}
	if err := c.writable(); err != nil {
		return nil, err
	}
	if c.pending != nil {
		return nil, InvalidState
	}
	if action != "create" && action != "rotate" && action != "revoke" {
		return nil, InvalidArgument
	}
	var old *credentialState
	if action != "create" {
		old = c.credentials[target]
		if old == nil {
			return nil, NotFound
		}
	}
	if action != "revoke" {
		if !ValidID(in.OperationID, "") {
			return nil, InvalidArgument
		}
		if in.ExpiresInSeconds == 0 {
			in.ExpiresInSeconds = int64(DefaultCredentialLifetime / time.Second)
		}
		if in.ExpiresInSeconds < int64(MinCredentialLifetime/time.Second) || in.ExpiresInSeconds > int64(MaxCredentialLifetime/time.Second) {
			return nil, InvalidArgument
		}
		if action == "create" {
			name, err := NormalizeCredentialName(in.Name)
			if err != nil {
				return nil, err
			}
			in.Name = name
			if (in.Kind != "access" && in.Kind != "mcp") || (in.Kind == "access" && in.AllowExec) {
				return nil, InvalidArgument
			}
		}
		if id := c.operations[in.OperationID]; id != "" {
			return nil, &Failure{Code: OperationExists, ResourceID: id}
		}
	}
	change := &CredentialChange{Revision: c.managementRevision, owner: auth.Digest(token), accepted: n}
	if action == "revoke" && old.Revoked {
		v := old.Credential
		change.Previous = &v
		change.Noop = true
		return change, nil
	}
	if c.managementRevision >= 9007199254740991 {
		return nil, CapacityExceeded
	}
	if action != "revoke" {
		if old != nil && old.Revoked {
			return nil, CredentialRevoked
		}
		if len(c.credentials) >= MaxCredentialRecords {
			return nil, CredentialLimit
		}
		if action == "rotate" {
			in.Kind, in.Name, in.AllowExec = old.Kind, old.Name, old.AllowExec
		}
		v := Credential{ID: newID("crd"), Kind: in.Kind, Name: in.Name, AllowExec: in.AllowExec,
			CreatedAt: n.Wall, ExpiresAt: n.Wall.Add(time.Duration(in.ExpiresInSeconds) * time.Second), CreationOperationID: in.OperationID}
		if old != nil {
			v.RotatedFromID = old.ID
		}
		change.Credential = &v
	}
	if old != nil {
		v := old.Credential
		v.Revoked = true
		v.RevokedAt = ptr(n.Wall)
		change.Previous = &v
		change.Effects = c.effects(old.ID)
		old.blocked = true
		c.invalidateCredential(old, n)
	}
	change.Revision++
	c.pending = change
	return change, nil
}

// CompleteCredential is called only after a confirmed durable commit. Even when
// response authority has expired, the committed projection must be installed.
func (c *Core) CompleteCredential(change *CredentialChange, digest [32]byte) (Credential, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if change.Noop && change.Revision == c.managementRevision && c.pending == nil {
		if _, ok := c.admins[change.owner]; !ok {
			return Credential{}, c.managementRevision, Unauthenticated
		}
		return c.credentialView(c.credentials[change.Previous.ID], c.countsByCredential()[change.Previous.ID]), c.managementRevision, nil
	}
	if c.pending != change {
		return Credential{}, c.managementRevision, InvalidState
	}
	if change.Previous != nil {
		old := c.credentials[change.Previous.ID]
		old.Credential = *change.Previous
	}
	var result *credentialState
	if v := change.Credential; v != nil {
		result = &credentialState{Credential: *v, expires: after(change.accepted, v.ExpiresAt.Sub(change.accepted.Wall)), expired: !n.Wall.Before(v.ExpiresAt)}
		if result.expires.due(n) {
			result.expired = true
		}
		c.credentials[v.ID] = result
		c.digests[digest] = v.ID
		c.operations[v.CreationOperationID] = v.ID
	} else {
		result = c.credentials[change.Previous.ID]
	}
	c.managementRevision = change.Revision
	c.pending = nil
	view := c.credentialView(result, c.countsByCredential()[result.ID])
	if _, ok := c.admins[change.owner]; !ok {
		return view, c.managementRevision, Unauthenticated
	}
	if err := c.writable(); err != nil {
		return view, c.managementRevision, err
	}
	if change.Credential != nil && result.expired {
		return view, c.managementRevision, StateUnavailable
	}
	return view, c.managementRevision, nil
}

func (c *Core) FailCredential(change *CredentialChange) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if c.pending == change {
		c.pending = nil
		c.halt("degraded", "server_degraded", n)
	}
}

func (c *Core) AdminNote(token, csrf, sid, note string, expected int64) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if _, err := c.admin(token, csrf, true); err != nil {
		return Session{}, err
	}
	s, err := c.activeSession(sid)
	if err != nil {
		return Session{}, err
	}
	if !validText(note, 0, 512) || expected < 0 {
		return Session{}, InvalidArgument
	}
	for _, r := range note {
		if r < 32 || r == 127 {
			return Session{}, InvalidArgument
		}
	}
	if expected != s.NoteRevision {
		return Session{}, &Failure{Code: RevisionConflict, CurrentRevision: ptr(s.NoteRevision)}
	}
	if s.Note != note {
		s.Note = note
		s.NoteRevision++
	}
	return sessionView(s, n), nil
}

func (c *Core) AdminEndSession(token, csrf, sid string) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if _, err := c.admin(token, csrf, true); err != nil {
		return Session{}, err
	}
	s := c.sessions[sid]
	if s == nil {
		return Session{}, NotFound
	}
	c.endSession(s, "admin_end", "session_ended", n)
	return sessionView(s, n), nil
}

func (c *Core) AdminEndClient(token, csrf, id string) (Client, Effects, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if _, err := c.admin(token, csrf, true); err != nil {
		return Client{}, Effects{}, err
	}
	cl := c.clients[id]
	if cl == nil {
		return Client{}, Effects{}, NotFound
	}
	var e Effects
	if cl.Lifecycle != "ended" {
		e.ClientsEnded = 1
		for _, s := range c.sessions {
			for _, t := range s.tasks {
				if t.ClientID == id && t.Phase != "settled" && t.Stop == nil {
					e.TasksStopRequested++
				}
			}
		}
	}
	c.endClient(cl, "admin_end", "mcp_client_ended", n)
	return clientView(cl, n), e, nil
}
