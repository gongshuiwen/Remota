package agent

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGrantUsesFirstSendAnchorAndChecksImmutableSpec(t *testing.T) {
	s, f, _, _, _ := activeState(t)
	offerEvent := fixture(t, "event.offer")
	offer := readOffer(offerEvent["payload"].(map[string]any)["task"].(map[string]any))
	response := fixture(t, "start.response")
	s.mu.Lock()
	s.mode = "full"
	s.mu.Unlock()
	sent := f.now()
	f.advance(2*time.Second, 2*time.Second)
	_, start, execution, ok := s.validateGrant(sent, offer, response)
	if !ok || start != sent.Elapsed+5*time.Second || execution != sent.Elapsed+10*time.Second {
		t.Fatalf("grant deadlines moved from first send: %v %v %v", start, execution, ok)
	}
	grant := response["grant"].(map[string]any)
	grant["spec"].(map[string]any)["command"] = "changed"
	if _, _, _, ok = s.validateGrant(sent, offer, response); ok {
		t.Fatal("changed command accepted")
	}
	grant["spec"].(map[string]any)["command"] = offer.spec.command
	grant["start_window_ms"] = json.Number("5001")
	if _, _, _, ok = s.validateGrant(sent, offer, response); ok {
		t.Fatal("oversized start window accepted")
	}
}

func TestDiscardStopsOutputCapture(t *testing.T) {
	var output limitedOutput
	_, _ = output.Write([]byte("before"))
	output.discard()
	_, _ = output.Write([]byte("after"))
	if got := output.value(); got.capturedBytes != 0 || got.text != "" || got.truncated {
		t.Fatalf("discarded output was retained: %+v", got)
	}
}

func TestTaskDeadlineUsesSuspendAwareElapsedTime(t *testing.T) {
	s, f, _, _, _ := activeState(t)
	ctx, cancel := context.WithCancel(context.Background())
	task := &runningTask{id: "task", cancel: cancel, stop: make(chan time.Duration, 1), done: make(chan struct{}), deadline: f.now().Elapsed + 5*time.Second}
	s.mu.Lock()
	s.task, s.tasks[task.id] = task, task
	s.mu.Unlock()
	f.advance(0, 5*time.Second)
	if _, ok := s.sample(); !ok {
		t.Fatal("task deadline ended the still-valid session")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("task survived its elapsed-time deadline")
	}
	select {
	case deadline := <-task.stop:
		if deadline != f.now().Elapsed+5*time.Second {
			t.Fatalf("wrong stop deadline: %s", deadline)
		}
	default:
		t.Fatal("task was not stopped")
	}
}

func TestTaskReportsRespectRetryAfter(t *testing.T) {
	for _, kind := range []string{"receipt", "result"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, _, _ := activeState(t)
			s.mode = "full"
			response := fixture(t, "task.result.response")
			var requests atomic.Int32
			times := make(chan time.Time, 2)
			web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				times <- time.Now()
				if requests.Add(1) == 1 {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(503)
					return
				}
				w.Header().Set("X-Remota-Epoch", strings.Repeat("a", 32))
				w.Header().Set("X-Request-ID", "test-request")
				w.Header().Set("Content-Type", "application/json")
				jsonv2.MarshalWrite(w, response)
			}))
			defer web.Close()
			transport, err := NewTransport(web.URL, false)
			if err != nil {
				t.Fatal(err)
			}
			a := &Agent{transport: transport}
			offer := readOffer(fixture(t, "start.response")["task"].(map[string]any))
			if kind == "receipt" {
				a.receipt(s, s.session.id, "token", strings.Repeat("a", 32), offer.id, fixture(t, "receipt.exited"))
			} else if !a.result(s, &runningTask{}, s.session.id, "token", strings.Repeat("a", 32), offer, fixture(t, "result.succeeded")) {
				t.Fatal("result was not accepted")
			}
			first, second := <-times, <-times
			if second.Sub(first) < time.Second {
				t.Fatalf("retried after %s; Retry-After requires one second", second.Sub(first))
			}
		})
	}
}

func TestTaskStopRetainsFirstCauseAndTightensCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	report, stopReport := context.WithCancel(context.Background())
	defer cancel()
	defer stopReport()
	task := &runningTask{cancel: cancel, stop: make(chan time.Duration, 1), process: &managedProcess{}, resultCancel: stopReport}
	stopRunningTask(task, "operator_cancel", 5*time.Second)
	_, _ = task.process.stdoutData.Write([]byte("private"))
	stopRunningTask(task, "credential_invalid", 7*time.Second)
	stopRunningTask(task, "execution_deadline", 3*time.Second)
	if ctx.Err() == nil || report.Err() == nil || task.reason != "operator_cancel" || !task.outputDenied || <-task.stop != 3*time.Second {
		t.Fatal("stop lost its first cause, output prohibition, or shortest deadline")
	}
	if output := task.process.stdoutData.value(); output.text != "" || output.capturedBytes != 0 {
		t.Fatal("revoked output was retained")
	}
}

func TestResultRetryStopsWhenTaskIdentityIsInvalidated(t *testing.T) {
	s, _, _, _, _ := activeState(t)
	s.mode = "full"
	offer := readOffer(fixture(t, "start.response")["task"].(map[string]any))
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := &runningTask{id: offer.id, cancel: cancel, stop: make(chan time.Duration, 1)}
	s.tasks[task.id] = task
	requested := make(chan struct{})
	var requests atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(requested)
		}
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(503)
	}))
	defer web.Close()
	transport, err := NewTransport(web.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{transport: transport}
	done := make(chan bool, 1)
	body := fixture(t, "result.succeeded")
	go func() { done <- a.result(s, task, s.session.id, "token", strings.Repeat("a", 32), offer, body) }()
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("result was not submitted")
	}
	s.stopTask(task.id, "credential_invalid", 5*time.Second)
	select {
	case accepted := <-done:
		if accepted {
			t.Fatal("invalid identity accepted an output result")
		}
	case <-time.After(time.Second):
		t.Fatal("result retry ignored authorization loss")
	}
	if requests.Load() != 1 {
		t.Fatal("result was retried after authorization loss")
	}
}

func TestTaskEventDeduplication(t *testing.T) {
	for _, cause := range []string{"duplicate_offer", "changed_offer", "cancel_before_offer", "duplicate_cancel", "changed_cancel"} {
		t.Run(cause, func(t *testing.T) {
			s, _, _, _, _ := activeState(t)
			s.mode = "full"
			first, second := fixture(t, "event.offer"), fixture(t, "event.offer")
			expectStart := cause == "duplicate_offer" || cause == "changed_offer"
			if !expectStart {
				first, second = fixture(t, "event.cancel"), fixture(t, "event.cancel")
			}
			if cause == "changed_offer" {
				second["payload"].(map[string]any)["task"].(map[string]any)["spec"].(map[string]any)["command"] = "changed"
			}
			if cause == "changed_cancel" {
				second["payload"].(map[string]any)["reason"] = "execution_deadline"
			}
			batches := [][]any{{first}, {second}}
			if cause == "cancel_before_offer" {
				batches = [][]any{{first, fixture(t, "event.offer")}}
			}
			responses := make([]map[string]any, len(batches))
			for i, events := range batches {
				response := fixture(t, "poll.full")
				lease := response["lease"].(map[string]any)
				lease["seq"], lease["issued_at"], lease["expires_at"] = i+2, "2026-11-08T04:00:06.000Z", "2026-11-08T04:00:36.000Z"
				response["events"] = events
				responses[i] = response
			}
			taskResponse := fixture(t, "task.result.response")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var starts, polls atomic.Int32
			startSeen := make(chan struct{})
			web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Remota-Epoch", strings.Repeat("a", 32))
				w.Header().Set("X-Request-ID", "test-request")
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/poll"):
					i := int(polls.Add(1)) - 1
					if i >= len(responses) {
						cancel()
						return
					}
					if i == 1 && expectStart {
						select {
						case <-startSeen:
						case <-ctx.Done():
							return
						}
					}
					jsonv2.MarshalWrite(w, responses[i])
				case strings.HasSuffix(r.URL.Path, "/start"):
					if starts.Add(1) == 1 {
						close(startSeen)
					}
					w.WriteHeader(503)
				case strings.HasSuffix(r.URL.Path, "/receipts"):
					jsonv2.MarshalWrite(w, taskResponse)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer web.Close()
			defer func() {
				s.end("local_end")
				s.mu.Lock()
				tasks := make([]*runningTask, 0, len(s.tasks))
				for _, task := range s.tasks {
					tasks = append(tasks, task)
				}
				s.mu.Unlock()
				for _, task := range tasks {
					select {
					case <-task.done:
					case <-time.After(3 * time.Second):
						t.Error("task cleanup did not finish")
					}
				}
			}()
			transport, err := NewTransport(web.URL, false)
			if err != nil {
				t.Fatal(err)
			}
			a := &Agent{transport: transport}
			err = a.poll(ctx, s, s.session.id, "token", strings.Repeat("a", 32))
			invalid := cause == "changed_offer" || cause == "changed_cancel"
			if invalid != errors.Is(err, ErrProtocol) {
				t.Fatalf("event validation: %v", err)
			}
			want := int32(0)
			if expectStart {
				want = 1
			}
			if starts.Load() != want {
				t.Fatalf("start requests=%d, want=%d", starts.Load(), want)
			}
			s.end("local_end")
		})
	}
}
