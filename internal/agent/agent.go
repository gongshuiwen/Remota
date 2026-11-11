package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"remota.local/remota/internal/auth"
	"remota.local/remota/internal/clock"
	"remota.local/remota/internal/protocol"
)

const Version = "0.1.0-dev"

type Agent struct {
	transport *Transport
	clock     clock.Source
	info      func() (map[string]any, error)
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func New(address string, allowInsecure bool) (*Agent, error) {
	t, err := NewTransport(address, allowInsecure)
	if err != nil {
		return nil, err
	}
	clockSource, err := clock.NewReal()
	if err != nil {
		return nil, err
	}
	return &Agent{transport: t, clock: clockSource, info: localInfo}, nil
}

// Run receives decisions from the local terminal. It never grants full consent
// or starts a command; the caller must require an interactive terminal.
func (a *Agent) Run(ctx context.Context, access string, input <-chan string, output io.Writer) error {
	output = &lockedWriter{w: output}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	state := newLocalState(a.clock, cancel)
	sent, ok := state.sample()
	if !ok {
		return ErrProtocol
	}
	request, stop := context.WithTimeout(work, 10*time.Second)
	registration, epoch, err := a.transport.request(request, "POST", "/sessions", access, "", map[string]any{}, 201, "Registration")
	cancelled := request.Err() != nil
	stop()
	access = ""
	if err != nil || cancelled {
		if err == nil {
			err = context.DeadlineExceeded
		}
		return fmt.Errorf("registration failed; a pending session may remain: %w", err)
	}
	token := registration["agent_token"].(string)
	if !auth.ValidSecret(token, "agent") {
		return ErrProtocol
	}
	if state.register(sent, registration) != nil {
		state.end("protocol_error")
		sid := registration["session"].(map[string]any)["id"].(string)
		a.closeSession(state, sid, token, epoch)
		return ErrProtocol
	}
	sid := state.session.id
	defer a.closeSession(state, sid, token, epoch)
	go state.watch()
	go func() {
		select {
		case <-ctx.Done():
			state.end("signal")
		case <-state.done:
		}
	}()
	if _, err = fmt.Fprintf(output, "Server: %q\nOperator: %q\nPurpose: temporary remote assistance\nExpires: %s\nPermissions: read-only ping and system information; no commands.\nPress Enter to accept, or type n to reject: ",
		a.transport.base, state.session.operator, state.session.expires.UTC().Format("2006-01-02T15:04:05.000Z")); err != nil {
		state.end("protocol_error")
		return err
	}
	for {
		select {
		case <-state.done:
			return nil
		case line, open := <-input:
			if !open {
				state.end("stdin_eof")
				return nil
			}
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "n", "no", "q":
				state.end("")
				a.reject(sid, token, epoch)
				return nil
			case "", "y", "yes":
				goto accepted
			default:
				fmt.Fprint(output, "Press Enter to accept read-only access, or n to reject: ")
			}
		}
	}

accepted:
	go a.actions(work, state, input, output, sid, token, epoch)
	for {
		sent, ok = state.sample()
		if !ok {
			return nil
		}
		request, stop = context.WithTimeout(work, 10*time.Second)
		decision, _, err := a.transport.request(request, "PUT", "/sessions/"+sid+"/decision", token, epoch,
			map[string]any{"decision": "accept", "mode": "readonly"}, 200, "DecisionResponse")
		cancelled := request.Err() != nil
		stop()
		if err == nil && !cancelled {
			if state.decision(sent, decision) != nil {
				state.end("protocol_error")
				return ErrProtocol
			}
			break
		}
		if work.Err() != nil {
			return nil
		}
		if !transient(err) && !cancelled {
			state.end("protocol_error")
			return err
		}
		if !retryWait(work, err) {
			return nil
		}
	}
	fmt.Fprintln(output, "Read-only consent confirmed; waiting for the first lease. Type q to end or r to keep read-only mode.")
	return a.poll(work, state, sid, token, epoch)
}

func (s *localState) watch() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			if _, ok := s.sample(); !ok {
				return
			}
		}
	}
}

func transient(err error) bool {
	if err == nil || errors.Is(err, ErrProtocol) {
		return false
	}
	var remote *RemoteError
	if errors.As(err, &remote) {
		return remote.Status >= 500 || remote.Status == 429 || remote.Code == "ALREADY_POLLING"
	}
	return true
}

func retryWait(ctx context.Context, err error) bool {
	delay := 500 * time.Millisecond
	var remote *RemoteError
	if errors.As(err, &remote) && remote.RetryAfter > delay {
		delay = remote.RetryAfter
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (a *Agent) reject(sid, token, epoch string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a.transport.request(ctx, "PUT", "/sessions/"+sid+"/decision", token, epoch, map[string]any{"decision": "reject"}, 200, "DecisionResponse")
}

func (a *Agent) closeSession(s *localState, sid, token, epoch string) {
	s.mu.Lock()
	reason := s.reason
	s.mu.Unlock()
	if reason == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a.transport.request(ctx, "POST", "/sessions/"+sid+"/close", token, epoch, map[string]any{"reason": reason}, 200, "SessionResponse")
}

func (a *Agent) actions(ctx context.Context, state *localState, input <-chan string, output io.Writer, sid, token, epoch string) {
	for {
		select {
		case <-state.done:
			return
		case line, open := <-input:
			if !open {
				state.end("stdin_eof")
				return
			}
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "q", "quit":
				state.end("local_end")
				return
			case "r", "readonly":
				state.mu.Lock()
				_, ok := state.sampleLocked()
				revision, generation := state.revision, state.generation
				if state.reducing {
					ok = false
				} else if ok && revision != 0 {
					state.reducing = true
				}
				state.mu.Unlock()
				if !ok || revision == 0 {
					continue
				}
				go a.reduce(ctx, state, output, sid, token, epoch, revision, generation)
			}
		}
	}
}

func (a *Agent) reduce(ctx context.Context, state *localState, output io.Writer, sid, token, epoch string, revision, generation int64) {
	defer func() {
		state.mu.Lock()
		state.reducing = false
		state.mu.Unlock()
	}()
	request, cancel := context.WithTimeout(ctx, 10*time.Second)
	v, _, err := a.transport.request(request, "PUT", "/sessions/"+sid+"/consent", token, epoch,
		map[string]any{"expected_revision": revision, "mode": "readonly"}, 200, "SessionResponse")
	cancelled := request.Err() != nil
	cancel()
	if err == nil && !cancelled {
		session, e := readSession(v["session"])
		state.mu.Lock()
		current := state.phase != "ended" && state.generation == generation
		valid := e == nil && sameSession(state.session, session) && session.state == "active" && session.mode == "readonly" && session.revision == revision
		state.mu.Unlock()
		if !current {
			return
		}
		if !valid {
			state.end("protocol_error")
			return
		}
		fmt.Fprintln(output, "Read-only mode confirmed.")
	} else if ctx.Err() == nil {
		if !transient(err) && !cancelled {
			state.end("protocol_error")
			return
		}
		state.pollFailed(generation)
	}
}

type cachedCall struct {
	method   string
	revision int64
	deadline time.Duration
	absolute time.Time
	result   map[string]any
}

type eventIdentity struct {
	created, deadline, callID, method string
	revision                          int64
}

type seenEvent struct {
	value    eventIdentity
	deadline time.Duration
}

func (a *Agent) poll(ctx context.Context, state *localState, sid, token, epoch string) error {
	acks := []string{}
	seen := make(map[string]seenEvent)
	calls := make(map[string]cachedCall)
	for {
		sent, revision, generation, ok := state.pollStart()
		if !ok {
			return nil
		}
		request, cancel := context.WithTimeout(ctx, 15*time.Second)
		body := map[string]any{"consent_revision": revision, "ack_event_ids": acks}
		response, _, err := a.transport.request(request, "POST", "/sessions/"+sid+"/poll", token, epoch, body, 200, "PollResult")
		if err != nil {
			cancelled := request.Err() != nil
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			state.pollFailed(generation)
			if !transient(err) && !cancelled {
				state.end("protocol_error")
				return err
			}
			if !retryWait(ctx, err) {
				return nil
			}
			continue
		}
		issued, err := state.acceptPoll(request, sent, generation, response)
		cancel()
		if errors.Is(err, errStale) {
			state.pollFailed(generation)
			continue
		}
		if err != nil {
			state.end("protocol_error")
			return err
		}
		if issued.IsZero() {
			return nil
		}
		acks = []string{}
		now, ok := state.sample()
		if !ok {
			return nil
		}
		for id, item := range seen {
			if now.Elapsed >= item.deadline {
				delete(seen, id)
			}
		}
		for id, item := range calls {
			if now.Elapsed >= item.deadline {
				delete(calls, id)
			}
		}
		for _, raw := range response["events"].([]any) {
			event := raw.(map[string]any)
			if event["session_id"] != sid || event["type"] != "call.request" {
				state.end("protocol_error")
				return ErrProtocol
			}
			absolute, err := time.Parse(time.RFC3339Nano, event["deadline"].(string))
			created, createdErr := time.Parse(time.RFC3339Nano, event["created_at"].(string))
			if err != nil || createdErr != nil || created.Before(state.session.created) || created.After(issued) || !absolute.After(created) || absolute.After(state.session.expires) {
				state.end("protocol_error")
				return ErrProtocol
			}
			deadline := sent.Elapsed
			if remaining := min(absolute.Sub(issued), absolute.Sub(created)).Milliseconds(); remaining > 0 {
				var valid bool
				deadline, valid = budget(sent.Elapsed, remaining)
				if !valid {
					state.end("protocol_error")
					return ErrProtocol
				}
			}
			payload := event["payload"].(map[string]any)
			callRevision := protocol.Integer(payload["consent_revision"])
			if callRevision != revision {
				state.end("protocol_error")
				return ErrProtocol
			}
			id, callID, method := event["id"].(string), payload["call_id"].(string), payload["method"].(string)
			identity := eventIdentity{event["created_at"].(string), event["deadline"].(string), callID, method, callRevision}
			if previous, ok := seen[id]; ok && previous.value != identity {
				state.end("protocol_error")
				return ErrProtocol
			}
			if previous, ok := seen[id]; ok {
				deadline = min(deadline, previous.deadline)
			}
			seen[id] = seenEvent{identity, deadline}
			acks = append(acks, id)
			call, exists := calls[callID]
			if exists && (call.method != method || call.revision != callRevision || !call.absolute.Equal(absolute)) {
				state.end("protocol_error")
				return ErrProtocol
			}
			if !exists {
				call = cachedCall{method: method, revision: revision, deadline: deadline, absolute: absolute}
				if state.permit(revision, deadline) {
					call.result = a.diagnose(state, method, revision, deadline)
				}
				calls[callID] = call
			} else if deadline < call.deadline {
				call.deadline = deadline
				calls[callID] = call
			}
			if call.result != nil && !a.sendCall(ctx, state, sid, token, epoch, callID, call) {
				return nil
			}
		}
	}
}

func (a *Agent) diagnose(s *localState, method string, revision int64, deadline time.Duration) map[string]any {
	if !s.permit(revision, deadline) {
		return nil
	}
	var result map[string]any
	if method == "ping" {
		n, ok := s.sample()
		if !ok {
			return nil
		}
		result = map[string]any{"pong": true, "agent_time": n.Wall.UTC().Format("2006-01-02T15:04:05.000Z")}
	} else {
		n, ok := s.sample()
		if !ok || n.Elapsed >= deadline {
			return nil
		}
		type infoResult struct {
			value map[string]any
			err   error
		}
		done := make(chan infoResult, 1)
		go func() {
			if !s.permit(revision, deadline) {
				done <- infoResult{err: ErrProtocol}
				return
			}
			info, err := a.info()
			done <- infoResult{value: info, err: err}
		}()
		timer := time.NewTimer(deadline - n.Elapsed)
		defer timer.Stop()
		var info infoResult
		select {
		case <-s.done:
			return nil
		case <-timer.C:
			return nil
		case info = <-done:
		}
		if info.err != nil {
			return map[string]any{"consent_revision": revision, "error": map[string]any{"code": "INTERNAL_ERROR"}}
		}
		result = info.value
	}
	if !s.permit(revision, deadline) {
		return nil
	}
	return map[string]any{"consent_revision": revision, "result": result}
}

func (a *Agent) sendCall(ctx context.Context, s *localState, sid, token, epoch, id string, call cachedCall) bool {
	for s.permit(call.revision, call.deadline) {
		n, ok := s.sample()
		if !ok {
			return false
		}
		request, cancel := context.WithTimeout(ctx, min(10*time.Second, call.deadline-n.Elapsed))
		_, _, err := a.transport.request(request, "POST", "/sessions/"+sid+"/calls/"+id+"/result", token, epoch, call.result, 204, "")
		cancelled := request.Err() != nil
		cancel()
		if err == nil && !cancelled {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		var remote *RemoteError
		if errors.As(err, &remote) && remote.Code == "CALL_EXPIRED" {
			return true
		}
		if !transient(err) && !cancelled {
			s.end("protocol_error")
			return false
		}
		if !retryWait(ctx, err) {
			return false
		}
	}
	return true
}

func localInfo() (map[string]any, error) {
	hostname, err := os.Hostname()
	if err != nil || !utf8.ValidString(hostname) || len(hostname) > 255 {
		return nil, errors.New("hostname unavailable")
	}
	shells := []string{}
	for _, shell := range []string{"sh", "powershell"} {
		if _, err := exec.LookPath(shell); err == nil {
			shells = append(shells, shell)
		}
	}
	return map[string]any{"agent_version": Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "hostname": hostname, "supported_shells": shells}, nil
}
