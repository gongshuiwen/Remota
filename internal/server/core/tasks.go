package core

import (
	"strings"
	"time"
	"unicode/utf8"

	"remota.local/remota/internal/clock"
)

func validID(s, prefix string) bool {
	if prefix != "" {
		prefix += "_"
	}
	if len(s) != len(prefix)+32 || !strings.HasPrefix(s, prefix) {
		return false
	}
	for _, b := range s[len(prefix):] {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

func validSpec(s Spec) bool {
	return utf8.ValidString(s.Command) && utf8.ValidString(s.Reason) &&
		len(s.Command) <= 4096 && !strings.ContainsRune(s.Command, 0) &&
		len(s.Reason) <= 512 && strings.TrimFunc(s.Command, whiteSpace) != "" &&
		strings.TrimFunc(s.Reason, whiteSpace) != "" &&
		(s.Shell == "auto" || s.Shell == "sh" || s.Shell == "powershell") &&
		s.TimeoutSeconds >= 1 && s.TimeoutSeconds <= 120
}

// Frozen contract White_Space, independent of the host Unicode version.
func whiteSpace(r rune) bool {
	return r >= '\t' && r <= '\r' || r == ' ' || r == '\u0085' || r == '\u00a0' ||
		r == '\u1680' || r >= '\u2000' && r <= '\u200a' || r == '\u2028' ||
		r == '\u2029' || r == '\u202f' || r == '\u205f' || r == '\u3000'
}

func (c *Core) executionGate(s *sessionState, revision int64) error {
	if s.State == "ended" {
		return SessionEnded
	}
	if s.State != "active" {
		return ConsentRequired
	}
	if revision != s.Consent.Revision {
		return RevisionConflict
	}
	if s.Consent.Mode != "full" {
		return ConsentRequired
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

func (c *Core) taskExecutionGate(s *sessionState, t *taskState, revision int64) error {
	if err := c.executionGate(s, revision); err != nil {
		return err
	}
	if t.ConsentRevision != revision {
		return RevisionConflict
	}
	v, err := c.client(t.CredentialID, t.ClientID)
	if err != nil {
		return err
	}
	if !v.AllowExec {
		return ExecNotAllowed
	}
	return nil
}

func (c *Core) Submit(credentialID, clientID, sid, requestID string, spec Spec) (Task, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	v, err := c.client(credentialID, clientID)
	if err != nil {
		return Task{}, false, err
	}
	if !v.AllowExec {
		return Task{}, false, ExecNotAllowed
	}
	s := c.sessions[sid]
	if s == nil {
		return Task{}, false, NotFound
	}
	revision := int64(0)
	if s.Consent != nil {
		revision = s.Consent.Revision
	}
	if err = c.executionGate(s, revision); err != nil {
		return Task{}, false, err
	}
	if !validID(requestID, "") || !validSpec(spec) {
		return Task{}, false, InvalidArgument
	}
	if id, ok := s.requests[requestID]; ok {
		t := s.tasks[id]
		if t.CredentialID != credentialID || t.Spec == nil || *t.Spec != spec {
			return Task{}, false, IdempotencyConflict
		}
		return taskView(t, false), true, nil
	}
	if len(s.tasks) >= 32 {
		return Task{}, false, TaskLimit
	}
	for _, t := range s.tasks {
		if t.Phase != "settled" || t.Execution.State == "unknown" || t.Execution.State == "may_have_started" || t.Execution.State == "started" {
			return Task{}, false, TaskBusy
		}
	}
	d := earlier(after(n, offerWait), s.expires)
	t := &taskState{Task: Task{ID: newID("tsk"), SessionID: sid, RequestID: requestID, Phase: "queued",
		CredentialID: credentialID, ClientID: clientID, ConsentRevision: revision,
		CreatedAt: n.Wall, OfferDeadline: d.wall, Execution: Execution{State: "not_started"},
		DetailsAvailable: true, Spec: ptr(spec)}, offer: d, receipts: make(map[string]Receipt)}
	t.offerEvent = &Event{ID: newID("evt"), Type: "task.offer", SessionID: sid,
		CreatedAt: n.Wall, Deadline: ptr(d.wall), Task: ptr(taskView(t, true))}
	s.tasks[t.ID], s.requests[requestID] = t, t.ID
	return taskView(t, false), false, nil
}

func (c *Core) Start(sid, tid string, revision int64) (Task, Grant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	s, t, err := c.task(sid, tid)
	if err != nil {
		return Task{}, Grant{}, err
	}
	if err = c.taskExecutionGate(s, t, revision); err != nil {
		return Task{}, Grant{}, err
	}
	if t.grant != nil && t.window.due(n) {
		return Task{}, Grant{}, GrantExpired
	}
	switch t.Phase {
	case "start_granted":
		return taskView(t, true), *t.grant, nil
	case "queued":
	default:
		return Task{}, Grant{}, InvalidState
	}
	t.execute = earlier(after(n, time.Duration(t.Spec.TimeoutSeconds)*time.Second), s.expires)
	t.window = earlier(earlier(after(n, startWindow), t.execute), s.lease)
	g := Grant{ID: newID("grt"), SessionID: sid, TaskID: tid, ConsentRevision: revision, Spec: *t.Spec,
		IssuedAt: n.Wall, ExecuteBefore: t.window.wall, ExecutionDeadline: t.execute.wall,
		StartWindowMS: t.window.remaining(n), ExecutionBudgetMS: t.execute.remaining(n)}
	t.grant = &g
	t.Phase, t.Execution.State, t.ExecutionDeadline = "start_granted", "may_have_started", ptr(t.execute.wall)
	return taskView(t, true), g, nil
}

func (c *Core) task(sid, tid string) (*sessionState, *taskState, error) {
	s := c.sessions[sid]
	if s == nil {
		return nil, nil, NotFound
	}
	t := s.tasks[tid]
	if t == nil {
		return nil, nil, TaskNotFound
	}
	return s, t, nil
}

func (c *Core) GetTask(credentialID, clientID, sid, taskID, requestID string) (Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	if _, err := c.client(credentialID, clientID); err != nil {
		return Task{}, err
	}
	if (taskID == "") == (requestID == "") {
		return Task{}, InvalidArgument
	}
	s := c.sessions[sid]
	if s == nil {
		return Task{}, NotFound
	}
	if taskID == "" {
		taskID = s.requests[requestID]
	}
	t := s.tasks[taskID]
	if t == nil {
		return Task{}, TaskNotFound
	}
	return taskView(t, true), nil
}

func (c *Core) Cancel(credentialID, clientID, sid, tid string) (Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if _, err := c.client(credentialID, clientID); err != nil {
		return Task{}, err
	}
	_, t, err := c.task(sid, tid)
	if err != nil {
		return Task{}, err
	}
	c.stopTask(t, "operator_cancel", n)
	return taskView(t, false), nil
}

func (c *Core) settle(t *taskState, outcome string, n clock.Sample) {
	if t.Phase == "settled" {
		return
	}
	t.Phase, t.Outcome, t.SettledAt = "settled", outcome, ptr(n.Wall)
}

func (c *Core) stopTask(t *taskState, reason string, n clock.Sample) {
	if t.Phase == "settled" || t.Stop != nil {
		return
	}
	if t.grant == nil {
		c.settle(t, "cancelled", n)
		return
	}
	t.confirm = after(n, stopWait)
	t.Stop = &Stop{Reason: reason, RequestedAt: n.Wall, ConfirmationDeadline: t.confirm.wall}
	t.Phase = "stopping"
	t.cancelEvent = &Event{ID: newID("evt"), Type: "task.cancel", SessionID: t.SessionID,
		CreatedAt: n.Wall, TaskID: t.ID, Reason: reason, ConfirmationDeadline: ptr(t.confirm.wall)}
}

func (c *Core) unknown(s *sessionState, t *taskState, n clock.Sample) {
	t.Execution.State, t.ErrorCode = "unknown", "PROCESS_STOP_UNCONFIRMED"
	t.Execution.ExitObservedAt, t.Execution.ExitCode = nil, nil
	c.settle(t, "unknown", n)
	c.endSession(s, "execution_unknown", "session_ended", n)
}

func stopOutcome(t *taskState, fallback string) string {
	if t.Stop == nil {
		return fallback
	}
	if t.Stop.Reason == "execution_deadline" {
		return "timed_out"
	}
	return "cancelled"
}

func taskView(t *taskState, details bool) Task {
	v := t.Task
	if !details || !v.DetailsAvailable {
		v.Spec, v.Result = nil, nil
	}
	return cloneTask(v)
}

func cloneTask(v Task) Task {
	v.Spec, v.Result, v.Stop = copyPtr(v.Spec), copyPtr(v.Result), copyPtr(v.Stop)
	v.ExecutionDeadline, v.SettledAt = copyPtr(v.ExecutionDeadline), copyPtr(v.SettledAt)
	v.Execution.StartObservedAt = copyPtr(v.Execution.StartObservedAt)
	v.Execution.ExitObservedAt = copyPtr(v.Execution.ExitObservedAt)
	v.Execution.ExitCode = copyPtr(v.Execution.ExitCode)
	return v
}
