package core

import "unicode/utf8"

func sameExit(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func validExit(v *int) bool   { return v == nil || (*v >= -9007199254740991 && *v <= 9007199254740991) }

func sameReceipt(a, b Receipt) bool {
	return a.Kind == b.Kind && a.GrantID == b.GrantID && a.Reason == b.Reason &&
		a.ErrorCode == b.ErrorCode && sameExit(a.ExitCode, b.ExitCode)
}

func validReceipt(r Receipt) bool {
	if r.GrantID != "" && !ValidID(r.GrantID, "grt") || !validExit(r.ExitCode) {
		return false
	}
	switch r.Kind {
	case "started":
		return r.GrantID != "" && r.ExitCode == nil && r.Reason == "" && r.ErrorCode == ""
	case "exited":
		return r.GrantID != "" && r.Reason == "" && r.ErrorCode == ""
	case "stop_unconfirmed":
		return r.GrantID != "" && r.ExitCode == nil && r.Reason == "" && r.ErrorCode == "PROCESS_STOP_UNCONFIRMED"
	case "not_started":
		if r.ExitCode != nil || r.ErrorCode != "" {
			return false
		}
		switch r.Reason {
		case "LOCAL_PERMISSION_DENIED", "UNSUPPORTED_SHELL", "SPAWN_FAILED", "GRANT_UNAVAILABLE", "GRANT_MISMATCH", "GRANT_EXPIRED", "CANCELLED_BEFORE_SPAWN", "SESSION_ENDED", "TRANSPORT_LOST":
			return true
		}
	}
	return false
}

func (c *Core) RecordReceipt(sid, tid string, r Receipt) (Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	s, t, err := c.task(sid, tid)
	if err != nil {
		return Task{}, err
	}
	if !validReceipt(r) {
		return Task{}, InvalidArgument
	}
	if old, ok := t.receipts[r.Kind]; ok {
		if !sameReceipt(old, r) {
			return Task{}, ResultConflict
		}
		return taskView(t, false), nil
	}
	if r.GrantID != "" && (t.grant == nil || t.grant.ID != r.GrantID) {
		return Task{}, ResultConflict
	}
	_, notStarted := t.receipts["not_started"]
	switch r.Kind {
	case "started":
		if notStarted {
			return Task{}, ResultConflict
		}
		t.Execution.StartObservedAt = ptr(n.Wall)
		if t.Phase != "settled" {
			t.Execution.State = "started"
			if t.Phase != "stopping" {
				t.Phase = "running"
			}
		}
	case "not_started":
		if t.Execution.StartObservedAt != nil || t.Execution.ExitObservedAt != nil {
			return Task{}, ResultConflict
		}
		t.Execution.State = "not_started"
		if t.Phase != "settled" {
			outcome := "cancelled"
			switch r.Reason {
			case "LOCAL_PERMISSION_DENIED", "UNSUPPORTED_SHELL":
				outcome = "rejected"
			case "SPAWN_FAILED":
				outcome = "failed"
			}
			t.ErrorCode = r.Reason
			c.settle(t, stopOutcome(t, outcome), n)
		}
	case "exited":
		if notStarted {
			return Task{}, ResultConflict
		}
		if t.Execution.ExitObservedAt != nil {
			if !sameExit(t.Execution.ExitCode, r.ExitCode) {
				return Task{}, ResultConflict
			}
		} else {
			t.Execution.State, t.Execution.ExitObservedAt, t.Execution.ExitCode = "exited", ptr(n.Wall), copyPtr(r.ExitCode)
			c.settle(t, stopOutcome(t, "unknown"), n)
		}
	case "stop_unconfirmed":
		if notStarted || t.Execution.ExitObservedAt != nil {
			return Task{}, ResultConflict
		}
		if t.Phase != "settled" {
			c.unknown(s, t, n)
		}
	}
	r.ExitCode = copyPtr(r.ExitCode)
	t.receipts[r.Kind] = r
	return taskView(t, false), nil
}

func validOutput(o Output) bool {
	return utf8.ValidString(o.Text) && len(o.Text) <= 24576 && o.CapturedBytes >= 0 && o.CapturedBytes <= 8192
}

func validResult(r Result) bool {
	if !ValidID(r.GrantID, "grt") || r.ConsentRevision < 1 || r.ConsentRevision > 9007199254740991 ||
		!validExit(r.ExitCode) || !validOutput(r.Stdout) || !validOutput(r.Stderr) {
		return false
	}
	if r.ExitCode == nil {
		if r.ErrorCode != "EXIT_CODE_UNAVAILABLE" {
			return false
		}
	} else if r.ErrorCode != "" {
		return false
	}
	switch r.Outcome {
	case "succeeded":
		return r.ExitCode != nil && *r.ExitCode == 0
	case "failed":
		return r.ExitCode == nil || *r.ExitCode != 0
	case "cancelled", "timed_out":
		return true
	}
	return false
}

func sameResult(a, b Result) bool {
	return a.GrantID == b.GrantID && a.ConsentRevision == b.ConsentRevision && a.Outcome == b.Outcome &&
		a.ErrorCode == b.ErrorCode && sameExit(a.ExitCode, b.ExitCode) && a.Stdout == b.Stdout && a.Stderr == b.Stderr
}

func (c *Core) RecordResult(sid, tid string, r Result) (Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	s, t, err := c.task(sid, tid)
	if err != nil {
		return Task{}, err
	}
	if err = c.taskExecutionGate(s, t, r.ConsentRevision); err != nil {
		return Task{}, err
	}
	if !validResult(r) {
		return Task{}, InvalidArgument
	}
	if t.grant == nil || r.GrantID != t.grant.ID {
		return Task{}, ResultConflict
	}
	if t.result != nil {
		if !sameResult(*t.result, r) {
			return Task{}, ResultConflict
		}
		return taskView(t, false), nil
	}
	if t.Phase == "settled" {
		return Task{}, ResultConflict
	}
	t.Execution.State, t.Execution.ExitObservedAt, t.Execution.ExitCode = "exited", ptr(n.Wall), copyPtr(r.ExitCode)
	t.ErrorCode, t.Result = r.ErrorCode, &TaskOutput{Stdout: r.Stdout, Stderr: r.Stderr}
	r.ExitCode = copyPtr(r.ExitCode)
	t.result = &r
	c.settle(t, stopOutcome(t, r.Outcome), n)
	return taskView(t, false), nil
}
