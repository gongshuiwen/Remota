package core

import (
	"reflect"
	"time"
)

type AgentInfo struct {
	Version, OS, Arch, Hostname string
	SupportedShells             []string
}

type DiagnosticResult struct {
	ConsentRevision int64
	Pong            bool
	AgentTime       time.Time
	Info            *AgentInfo
	ErrorCode       string
}

type DiagnosticCall struct {
	ID, SessionID, Method string
	ConsentRevision       int64
	Deadline              time.Time
	Result                *DiagnosticResult
	Done                  <-chan struct{}
}

type callState struct {
	DiagnosticCall
	credentialID, clientID string
	expires                deadline
	event                  Event
	acknowledged           bool
	done                   chan struct{}
}

func (c *Core) BeginDiagnostic(credentialID, clientID, sid, method string) (DiagnosticCall, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if _, err := c.client(credentialID, clientID); err != nil {
		return DiagnosticCall{}, err
	}
	s, err := c.activeSession(sid)
	if err != nil {
		return DiagnosticCall{}, err
	}
	if method != "ping" && method != "info" {
		return DiagnosticCall{}, InvalidArgument
	}
	if err = c.diagnosticGate(s, 0); err != nil {
		return DiagnosticCall{}, err
	}
	if len(s.calls) >= 16 {
		return DiagnosticCall{}, CapacityExceeded
	}
	d := earlier(after(n, callWait), s.expires)
	call := &callState{DiagnosticCall: DiagnosticCall{ID: newID("call"), SessionID: sid, Method: method,
		ConsentRevision: s.Consent.Revision, Deadline: d.wall}, credentialID: credentialID, clientID: clientID,
		expires: d, done: make(chan struct{})}
	call.event = Event{ID: newID("evt"), Type: "call.request", SessionID: sid, CreatedAt: n.Wall,
		Deadline: ptr(d.wall), CallID: call.ID, Method: method, ConsentRevision: call.ConsentRevision}
	s.calls[call.ID] = call
	c.notify(s)
	return callView(call), nil
}

func (c *Core) Diagnostic(credentialID, clientID, sid, id string) (DiagnosticCall, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	if _, err := c.client(credentialID, clientID); err != nil {
		return DiagnosticCall{}, err
	}
	s, err := c.activeSession(sid)
	if err != nil {
		return DiagnosticCall{}, err
	}
	call := s.calls[id]
	if call == nil {
		return DiagnosticCall{}, CallExpired
	}
	if call.credentialID != credentialID || call.clientID != clientID {
		return DiagnosticCall{}, NotFound
	}
	if err = c.diagnosticGate(s, call.ConsentRevision); err != nil {
		return DiagnosticCall{}, err
	}
	return callView(call), nil
}

func (c *Core) RecordDiagnostic(sid, id string, result DiagnosticResult) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	s, err := c.activeSession(sid)
	if err != nil {
		return err
	}
	call := s.calls[id]
	if call == nil {
		return CallExpired
	}
	if err = c.diagnosticGate(s, result.ConsentRevision); err != nil {
		return err
	}
	if result.ConsentRevision != call.ConsentRevision || !validDiagnostic(call.Method, result) {
		return InvalidArgument
	}
	result.Info = copyInfo(result.Info)
	if call.Result != nil {
		if !reflect.DeepEqual(*call.Result, result) {
			return ResultConflict
		}
		return nil
	}
	call.Result = &result
	finishCall(call)
	return nil
}

// A zero revision is used only when starting a diagnostic with current consent.
func (c *Core) diagnosticGate(s *sessionState, revision int64) error {
	if s.State != "active" {
		return ConsentRequired
	}
	if revision != 0 && revision != s.Consent.Revision {
		return RevisionConflict
	}
	if err := c.writable(); err != nil {
		return err
	}
	if _, err := c.credential(s.accessID, "access"); err != nil {
		return err
	}
	if s.leaseSeq == 0 {
		return AgentUnavailable
	}
	return nil
}

func validDiagnostic(method string, r DiagnosticResult) bool {
	if r.ErrorCode != "" {
		if r.Pong || r.Info != nil || !r.AgentTime.IsZero() {
			return false
		}
		switch r.ErrorCode {
		case "LOCAL_PERMISSION_DENIED", "UNSUPPORTED_DIAGNOSTIC", "DEADLINE_EXCEEDED", "INTERNAL_ERROR":
			return true
		}
		return false
	}
	if method == "ping" {
		return r.Pong && r.Info == nil
	}
	v := r.Info
	if method != "info" || r.Pong || !r.AgentTime.IsZero() || v == nil ||
		!validText(v.Version, 1, 80) || !validText(v.Hostname, 0, 255) ||
		(v.OS != "windows" && v.OS != "linux" && v.OS != "darwin") || (v.Arch != "amd64" && v.Arch != "arm64") {
		return false
	}
	seen := map[string]bool{}
	for _, shell := range v.SupportedShells {
		if (shell != "sh" && shell != "powershell") || seen[shell] {
			return false
		}
		seen[shell] = true
	}
	return true
}

func copyInfo(v *AgentInfo) *AgentInfo {
	if v == nil {
		return nil
	}
	r := *v
	r.SupportedShells = append([]string{}, v.SupportedShells...)
	return &r
}

func callView(call *callState) DiagnosticCall {
	v := call.DiagnosticCall
	if v.Result != nil {
		r := *v.Result
		r.Info = copyInfo(r.Info)
		v.Result = &r
	}
	v.Done = call.done
	return v
}

func finishCall(call *callState) {
	if call.done != nil {
		close(call.done)
		call.done = nil
	}
}

func (c *Core) clearCalls(s *sessionState) {
	for id, call := range s.calls {
		finishCall(call)
		delete(s.calls, id)
	}
}
