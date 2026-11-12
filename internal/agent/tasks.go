package agent

import (
	"context"
	"errors"
	"time"

	"remota.local/remota/internal/clock"
	"remota.local/remota/internal/protocol"
)

type taskOffer struct {
	id, session, request, created, expires, credential, client string
	revision                                                   int64
	spec                                                       commandSpec
}

type taskCancellation struct {
	created, reason, deadline string
}

type taskEventRecord struct {
	offerID, cancelID string
	offer             taskOffer
	cancel            taskCancellation
}

func readSpec(m map[string]any) commandSpec {
	return commandSpec{command: m["command"].(string), reason: m["reason"].(string), shell: m["shell"].(string), timeoutSeconds: protocol.Integer(m["timeout_seconds"])}
}

func readOffer(m map[string]any) taskOffer {
	initiator := m["initiator"].(map[string]any)
	return taskOffer{id: m["id"].(string), session: m["session_id"].(string), request: m["request_id"].(string),
		created: m["created_at"].(string), expires: m["offer_deadline"].(string), credential: initiator["credential_id"].(string),
		client: initiator["mcp_client_id"].(string), revision: protocol.Integer(m["consent_revision"]), spec: readSpec(m["spec"].(map[string]any))}
}

type runningTask struct {
	id           string
	stop         chan time.Duration // absolute deadline on the sleep-aware clock
	cancel       context.CancelFunc
	done         chan struct{}
	reason       string // guarded by localState.mu
	deadline     time.Duration
	stopDeadline time.Duration
	process      *managedProcess
	outputDenied bool
	resultCancel context.CancelFunc
}

func discardsOutput(reason string) bool {
	switch reason {
	case "SESSION_ENDED", "LOCAL_PERMISSION_DENIED", "TRANSPORT_LOST", "session_ended", "local_revoked", "credential_invalid", "mcp_client_ended", "transport_lost", "server_degraded":
		return true
	}
	return false
}

func notStartedReason(reason string) string {
	switch reason {
	case "timed_out", "execution_deadline":
		return "GRANT_EXPIRED"
	case "local_revoked":
		return "LOCAL_PERMISSION_DENIED"
	case "session_ended":
		return "SESSION_ENDED"
	case "transport_lost":
		return "TRANSPORT_LOST"
	case "operator_cancel", "credential_invalid", "mcp_client_ended", "server_degraded", "start_unconfirmed":
		return "CANCELLED_BEFORE_SPAWN"
	}
	return reason
}

func (s *localState) stopTaskLocked(reason string, window time.Duration) {
	for _, task := range s.tasks {
		stopRunningTask(task, reason, s.last+window)
	}
}

func stopRunningTask(task *runningTask, reason string, deadline time.Duration) {
	if discardsOutput(reason) {
		task.outputDenied = true
		if task.process != nil {
			task.process.discard()
		}
		if task.resultCancel != nil {
			task.resultCancel()
		}
	}
	if task.reason != "" && deadline >= task.stopDeadline {
		return
	}
	if task.reason == "" {
		task.reason = reason
		task.cancel()
	}
	task.stopDeadline = deadline
	// Senders hold localState.mu; replacing the one queued deadline cannot block.
	select {
	case <-task.stop:
	default:
	}
	task.stop <- deadline
}

func (s *localState) stopTask(id, reason string, deadline time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.tasks[id]; task != nil {
		stopRunningTask(task, reason, deadline)
	}
}

// Register cancellation before returning to the poll loop.
func (a *Agent) claimTask(ctx context.Context, s *localState, sid, token, epoch string, offer taskOffer, offerEnd time.Duration) {
	s.mu.Lock()
	_, ok := s.sampleLocked()
	if !ok || s.mode != "full" || s.revision != offer.revision {
		s.mu.Unlock()
		return
	}
	taskContext, cancel := context.WithCancel(ctx)
	task := &runningTask{id: offer.id, stop: make(chan time.Duration, 1), cancel: cancel, done: make(chan struct{})}
	s.tasks[offer.id] = task
	s.mu.Unlock()
	go a.runTask(taskContext, s, task, sid, token, epoch, offer, offerEnd)
}

func (a *Agent) runTask(ctx context.Context, s *localState, task *runningTask, sid, token, epoch string, offer taskOffer, offerEnd time.Duration) {
	defer func() {
		task.cancel()
		s.mu.Lock()
		if s.task == task {
			s.task = nil
		}
		delete(s.tasks, task.id)
		close(task.done)
		s.mu.Unlock()
	}()
	for {
		s.mu.Lock()
		_, ok := s.sampleLocked()
		reason := task.reason
		if !ok || s.mode != "full" || s.revision != offer.revision || reason != "" {
			s.mu.Unlock()
			if reason != "" {
				a.receipt(s, sid, token, epoch, offer.id, map[string]any{"kind": "not_started", "reason": notStartedReason(reason)})
			}
			return
		}
		if s.task == nil {
			s.task = task
			s.mu.Unlock()
			a.executeTask(ctx, s, task, sid, token, epoch, offer, offerEnd)
			return
		}
		done := s.task.done
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
}

func (a *Agent) executeTask(ctx context.Context, s *localState, task *runningTask, sid, token, epoch string, offer taskOffer, offerEnd time.Duration) {
	n, ok := s.sample()
	if !ok || !s.permit(offer.revision, offerEnd) {
		a.receipt(s, sid, token, epoch, offer.id, map[string]any{"kind": "not_started", "reason": "LOCAL_PERMISSION_DENIED"})
		return
	}
	// The first send is the only local anchor. Lost or late responses are never retried.
	request, cancel := context.WithTimeout(ctx, 5*time.Second)
	response, _, err := a.transport.request(request, "POST", "/sessions/"+sid+"/tasks/"+offer.id+"/start", token, epoch,
		map[string]any{"consent_revision": offer.revision}, 200, "StartResponse")
	cancelled := request.Err() != nil
	cancel()
	if err != nil || cancelled {
		if err != nil && !transient(err) && !cancelled {
			s.end("protocol_error")
		}
		a.receipt(s, sid, token, epoch, offer.id, map[string]any{"kind": "not_started", "reason": "GRANT_UNAVAILABLE"})
		return
	}
	grant, startEnd, executeEnd, valid := s.validateGrant(n, offer, response)
	if !valid {
		s.end("protocol_error")
		a.receipt(s, sid, token, epoch, offer.id, map[string]any{"kind": "not_started", "reason": "GRANT_MISMATCH"})
		return
	}
	process, failed := startProcess(offer.spec, func(p *managedProcess, start func() error) string {
		s.mu.Lock()
		defer s.mu.Unlock()
		now, live := s.sampleLocked()
		reason := task.reason
		if reason == "" && ctx.Err() != nil {
			return "SESSION_ENDED"
		}
		if !live || s.phase != "active" || s.mode != "full" || s.revision != offer.revision || reason != "" || now.Elapsed >= startEnd || now.Elapsed >= executeEnd {
			if reason == "" {
				return "GRANT_EXPIRED"
			}
			return notStartedReason(reason)
		}
		task.deadline = executeEnd
		if start() != nil {
			return "SPAWN_FAILED"
		}
		task.process = p
		return ""
	})
	if process == nil {
		a.receipt(s, sid, token, epoch, offer.id, map[string]any{"kind": "not_started", "reason": failed, "grant_id": grant})
		return
	}
	go a.receipt(s, sid, token, epoch, offer.id, map[string]any{"kind": "started", "grant_id": grant})
	result := process.wait(task.stop, s.clock)
	if !result.confirmed {
		s.end("protocol_error")
		a.receipt(s, sid, token, epoch, offer.id, map[string]any{"kind": "stop_unconfirmed", "grant_id": grant, "error_code": "PROCESS_STOP_UNCONFIRMED"})
		return
	}
	s.mu.Lock()
	_, live := s.sampleLocked()
	reason := task.reason
	full := live && s.phase == "active" && s.mode == "full" && s.revision == offer.revision && !task.outputDenied
	s.mu.Unlock()
	exited := map[string]any{"kind": "exited", "grant_id": grant}
	if result.exitCode != nil {
		exited["exit_code"] = *result.exitCode
	}
	if !full {
		a.receipt(s, sid, token, epoch, offer.id, exited)
		return
	}
	outcome := "failed"
	if reason == "timed_out" || reason == "execution_deadline" {
		outcome = "timed_out"
	} else if reason != "" {
		outcome = "cancelled"
	} else if result.exitCode != nil && *result.exitCode == 0 {
		outcome = "succeeded"
	}
	body := map[string]any{"grant_id": grant, "consent_revision": offer.revision, "outcome": outcome, "stdout": result.stdout.value(), "stderr": result.stderr.value()}
	if result.exitCode != nil {
		body["exit_code"] = *result.exitCode
	} else {
		body["error_code"] = "EXIT_CODE_UNAVAILABLE"
	}
	if !a.result(s, task, sid, token, epoch, offer, body) {
		a.receipt(s, sid, token, epoch, offer.id, exited)
	}
}

func (s *localState) validateGrant(sent clock.Sample, offer taskOffer, response map[string]any) (string, time.Duration, time.Duration, bool) {
	task, grant := response["task"].(map[string]any), response["grant"].(map[string]any)
	if readOffer(task) != offer || task["phase"] != "start_granted" || grant["session_id"] != offer.session || grant["task_id"] != offer.id ||
		protocol.Integer(grant["consent_revision"]) != offer.revision || readSpec(grant["spec"].(map[string]any)) != offer.spec {
		return "", 0, 0, false
	}
	issued, e1 := time.Parse(time.RFC3339Nano, grant["issued_at"].(string))
	before, e2 := time.Parse(time.RFC3339Nano, grant["execute_before"].(string))
	deadline, e3 := time.Parse(time.RFC3339Nano, grant["execution_deadline"].(string))
	created, e4 := time.Parse(time.RFC3339Nano, offer.created)
	offerDeadline, e5 := time.Parse(time.RFC3339Nano, offer.expires)
	window, execution := protocol.Integer(grant["start_window_ms"]), protocol.Integer(grant["execution_budget_ms"])
	s.mu.Lock()
	defer s.mu.Unlock()
	expectedDeadline := issued.Add(time.Duration(offer.spec.timeoutSeconds) * time.Second)
	if s.session.expires.Before(expectedDeadline) {
		expectedDeadline = s.session.expires
	}
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || issued.Before(created) || !issued.Before(offerDeadline) || !before.After(issued) || !deadline.After(issued) || before.After(deadline) || deadline.After(s.session.expires) ||
		!deadline.Equal(expectedDeadline) || before.Sub(issued) > 5*time.Second || before.After(s.leaseExpiry) ||
		window <= 0 || window > 5000 || execution <= 0 || execution > offer.spec.timeoutSeconds*1000 || window > execution ||
		window > before.Sub(issued).Milliseconds() || execution > deadline.Sub(issued).Milliseconds() || task["execution_deadline"] != grant["execution_deadline"] {
		return "", 0, 0, false
	}
	startEnd, ok1 := budget(sent.Elapsed, window)
	executeEnd, ok2 := budget(sent.Elapsed, execution)
	return grant["id"].(string), min(startEnd, s.sessionEnd), min(executeEnd, s.sessionEnd), ok1 && ok2
}

// Receipts are immutable and can still be sent after local authorization ends.
func (a *Agent) receipt(s *localState, sid, token, epoch, id string, body map[string]any) {
	cleanup, stop := context.WithTimeout(context.Background(), 6*time.Second)
	defer stop()
	for cleanup.Err() == nil {
		ctx, cancel := context.WithTimeout(cleanup, 2*time.Second)
		_, _, err := a.transport.request(ctx, "POST", "/sessions/"+sid+"/tasks/"+id+"/receipts", token, epoch, body, 200, "TaskResponse")
		cancel()
		var remote *RemoteError
		if errors.Is(err, ErrProtocol) || errors.As(err, &remote) && (remote.Status == 401 || remote.Status == 403) {
			s.end("protocol_error")
		}
		if err == nil || !transient(err) {
			return
		}
		if !retryWait(cleanup, err) {
			return
		}
	}
}

func (a *Agent) result(s *localState, task *runningTask, sid, token, epoch string, offer taskOffer, body map[string]any) bool {
	report, stop := context.WithCancel(context.Background())
	defer stop()
	s.mu.Lock()
	task.resultCancel = stop
	s.mu.Unlock()
	defer func() { s.mu.Lock(); task.resultCancel = nil; s.mu.Unlock() }()
	for {
		s.mu.Lock()
		_, ok := s.sampleLocked()
		full := ok && s.phase == "active" && s.mode == "full" && s.revision == offer.revision && !task.outputDenied
		if !full {
			s.mu.Unlock()
			return false
		}
		ctx, cancel := context.WithTimeout(report, 5*time.Second)
		s.mu.Unlock()
		_, _, err := a.transport.request(ctx, "POST", "/sessions/"+sid+"/tasks/"+offer.id+"/result", token, epoch, body, 200, "TaskResponse")
		cancelled := ctx.Err() != nil
		cancel()
		if err == nil && !cancelled {
			return true
		}
		if report.Err() != nil {
			return false
		}
		var remote *RemoteError
		if errors.As(err, &remote) && (remote.Status == 401 || remote.Status == 403 || remote.Status == 410) {
			s.end("protocol_error")
			return false
		}
		if !transient(err) && !cancelled {
			s.end("protocol_error")
			return false
		}
		if !retryWait(report, err) {
			return false
		}
	}
}
