//go:build linux || darwin

package agent

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remota.local/remota/internal/clock"
	"remota.local/remota/internal/protocol"
	"remota.local/remota/internal/server/core"
)

type fullAgent struct {
	backend                 *agentServer
	run                     *agentRun
	sid, credential, client string
	next                    int
}

func newFullAgent(t *testing.T, source clock.Source, wrap func(http.Handler) http.Handler) *fullAgent {
	t.Helper()
	backend := newAgentServer(t, source, wrap)
	a, err := New(backend.web.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	if source != nil {
		a.clock = source
	}
	run := startAgentRun(t, a, backend.handoff.Access, io.Discard)
	run.input <- "f"
	run.input <- "full"
	var sid string
	run.wait(t, func() bool {
		snapshot := backend.service.Core.Snapshot()
		if len(snapshot.Sessions) != 1 {
			return false
		}
		session := snapshot.Sessions[0]
		sid = session.ID
		return session.State == "active" && session.Consent.Mode == "full" && session.Activity == "recent"
	})
	credential, err := backend.service.Core.AuthenticateCredential(backend.handoff.MCP, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	client, err := backend.service.Core.BeginClient(credential, core.ClientInfo{Name: "test", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backend.service.Core.ClientMessage(credential, client.ID, true); err != nil {
		t.Fatal(err)
	}
	return &fullAgent{backend: backend, run: run, sid: sid, credential: credential, client: client.ID}
}

func (f *fullAgent) submit(t *testing.T, command string, seconds int) core.Task {
	t.Helper()
	f.next++
	task, _, err := f.backend.service.Core.Submit(f.credential, f.client, f.sid, fmt.Sprintf("%032x", f.next),
		core.Spec{Command: command, Reason: "integration test", Shell: "sh", TimeoutSeconds: seconds})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func (f *fullAgent) task(t *testing.T, id string) core.Task {
	t.Helper()
	task, err := f.backend.service.Core.GetTask(f.credential, f.client, f.sid, id, "")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func (f *fullAgent) meta(t *testing.T, id string) core.Task {
	t.Helper()
	for _, task := range f.backend.service.Core.Snapshot().Tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatal("task not found")
	return core.Task{}
}

func TestFullAgentResultReplyLost(t *testing.T) {
	var requests atomic.Int32
	var original any // accessed only by this task's sequential result requests
	f := newFullAgent(t, nil, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/result") && strings.Contains(r.URL.Path, "/tasks/") {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				value, err := protocol.Parse(body)
				if err != nil {
					t.Error(err)
					return
				}
				recorded := httptest.NewRecorder()
				next.ServeHTTP(recorded, r)
				if requests.Add(1) == 1 {
					original = value
					w.WriteHeader(503)
					return
				}
				if !reflect.DeepEqual(value, original) {
					t.Error("result retry changed its body")
				}
				replayResponse(w, recorded)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	marker := filepath.Join(t.TempDir(), "executed")
	task := f.submit(t, fmt.Sprintf("printf x >> '%s'; printf hello; printf warning >&2", marker), 30)
	f.run.wait(t, func() bool { return f.task(t, task.ID).Phase == "settled" && requests.Load() >= 2 })
	result := f.task(t, task.ID)
	if result.Outcome != "succeeded" || result.Result == nil || result.Result.Stdout.Text != "hello" ||
		result.Result.Stderr.Text != "warning" || result.Execution.ExitCode == nil || *result.Execution.ExitCode != 0 {
		t.Fatalf("result: %+v", result)
	}
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "x" {
		t.Fatalf("command was repeated: %q %v", content, err)
	}
	f.run.stop(t)
}

func TestFullAgentStartFailures(t *testing.T) {
	for _, cause := range []string{"lost_response", "cancel_before_response", "expired_response"} {
		t.Run(cause, func(t *testing.T) {
			source, err := clock.NewReal()
			if err != nil {
				t.Fatal(err)
			}
			var offset atomic.Int64
			now := func() clock.Sample {
				n := source()
				d := time.Duration(offset.Load())
				n.Wall = n.Wall.Add(d)
				n.Elapsed += d
				return n
			}
			held, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			var starts atomic.Int32
			f := newFullAgent(t, now, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/start") {
						starts.Add(1)
						recorded := httptest.NewRecorder()
						next.ServeHTTP(recorded, r)
						switch cause {
						case "lost_response":
							w.WriteHeader(503)
							return
						case "cancel_before_response":
							close(held)
							<-release
						case "expired_response":
							offset.Add(int64(5 * time.Second))
						}
						replayResponse(w, recorded)
						return
					}
					next.ServeHTTP(w, r)
				})
			})
			marker := filepath.Join(t.TempDir(), "must-not-exist")
			task := f.submit(t, fmt.Sprintf("printf x > '%s'", marker), 30)
			if cause == "cancel_before_response" {
				select {
				case <-held:
				case <-time.After(5 * time.Second):
					t.Fatal("start response was not held")
				}
				if _, err = f.backend.service.Core.Cancel(f.credential, f.client, f.sid, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			f.run.wait(t, func() bool { return f.task(t, task.ID).Phase == "settled" })
			result := f.task(t, task.ID)
			reason := "GRANT_UNAVAILABLE"
			if cause == "expired_response" {
				reason = "GRANT_EXPIRED"
			}
			if result.Execution.State != "not_started" || result.ErrorCode != reason || starts.Load() != 1 {
				t.Fatalf("start failure: %+v; requests=%d", result, starts.Load())
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("command was spawned: %v", err)
			}
			f.run.stop(t)
		})
	}
}

func TestFullAgentRemoteStops(t *testing.T) {
	for _, cause := range []string{"operator_cancel", "mcp_client_ended", "credential_invalid", "execution_deadline"} {
		t.Run(cause, func(t *testing.T) {
			var results atomic.Int32
			f := newFullAgent(t, nil, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/result") && strings.Contains(r.URL.Path, "/tasks/") {
						results.Add(1)
					}
					next.ServeHTTP(w, r)
				})
			})
			seconds := 30
			if cause == "execution_deadline" {
				seconds = 1
			}
			task := f.submit(t, "printf private; exec sleep 30", seconds)
			f.run.wait(t, func() bool { return f.meta(t, task.ID).Execution.State == "started" })
			var err error
			switch cause {
			case "operator_cancel":
				_, err = f.backend.service.Core.Cancel(f.credential, f.client, f.sid, task.ID)
			case "mcp_client_ended":
				_, err = f.backend.service.Core.EndClient(f.client, "client_delete")
			case "credential_invalid":
				admin, e := f.backend.service.Core.Login("")
				if e != nil {
					t.Fatal(e)
				}
				change, e := f.backend.service.Core.PrepareCredential(admin.Token, admin.CSRF, "revoke", f.credential, core.CredentialInput{})
				if e != nil {
					t.Fatal(e)
				}
				_, _, err = f.backend.service.Core.CompleteCredential(change, [32]byte{})
			}
			if err != nil {
				t.Fatal(err)
			}
			f.run.wait(t, func() bool { return f.meta(t, task.ID).Phase == "settled" })
			result := f.meta(t, task.ID)
			want := "cancelled"
			if cause == "execution_deadline" {
				want = "timed_out"
			}
			if result.Outcome != want || result.Execution.State != "exited" {
				t.Fatalf("stop result: %+v", result)
			}
			if cause == "mcp_client_ended" || cause == "credential_invalid" {
				if results.Load() != 0 {
					t.Fatal("output result sent after task identity was invalidated")
				}
			} else if results.Load() != 1 {
				t.Fatal("ordinary cancellation or timeout lost its full result")
			}
			f.run.stop(t)
		})
	}
}

func TestFullAgentLocalStops(t *testing.T) {
	for _, cause := range []string{"transport_lost", "readonly"} {
		t.Run(cause, func(t *testing.T) {
			var failPoll atomic.Bool
			f := newFullAgent(t, nil, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/poll") && failPoll.Swap(false) {
						w.WriteHeader(503)
						return
					}
					next.ServeHTTP(w, r)
				})
			})
			task := f.submit(t, "printf private; exec sleep 30", 30)
			f.run.wait(t, func() bool { return f.task(t, task.ID).Execution.State == "started" })
			if cause == "readonly" {
				f.run.input <- "r"
				f.run.wait(t, func() bool {
					v, e := f.backend.service.Core.GetSession(f.sid)
					return e == nil && v.Consent.Mode == "readonly" && v.Consent.Revision == 2
				})
			} else {
				failPoll.Store(true)
				if _, err := f.backend.service.Core.BeginDiagnostic(f.credential, f.client, f.sid, "ping"); err != nil {
					t.Fatal(err)
				}
			}
			f.run.wait(t, func() bool { return f.task(t, task.ID).Phase == "settled" })
			result := f.task(t, task.ID)
			if result.Execution.State != "exited" || result.Result != nil {
				t.Fatalf("output retained or exit missing: %+v", result)
			}
			f.run.stop(t)
		})
	}
}

func TestFullAgentRenewsLeaseDuringCommand(t *testing.T) {
	source, err := clock.NewReal()
	if err != nil {
		t.Fatal(err)
	}
	var offset atomic.Int64
	now := func() clock.Sample {
		n := source()
		d := time.Duration(offset.Load())
		n.Wall = n.Wall.Add(d)
		n.Elapsed += d
		return n
	}
	f := newFullAgent(t, now, nil)
	task := f.submit(t, "sleep 3; printf long", 60)
	f.run.wait(t, func() bool { return f.task(t, task.ID).Execution.State == "started" })
	for i := 0; i < 8; i++ {
		offset.Add(int64(5 * time.Second))
		if _, err = f.backend.service.Core.ClientMessage(f.credential, f.client, true); err != nil {
			t.Fatal(err)
		}
		call, err := f.backend.service.Core.BeginDiagnostic(f.credential, f.client, f.sid, "ping")
		if err != nil {
			t.Fatal(err)
		}
		// A diagnostic reply proves that the Agent accepted the refreshed lease.
		select {
		case <-call.Done:
		case <-time.After(3 * time.Second):
			t.Fatal("Agent did not acknowledge the renewed lease")
		}
	}
	f.run.wait(t, func() bool { return f.task(t, task.ID).Phase == "settled" })
	result := f.task(t, task.ID)
	if result.Outcome != "succeeded" || result.Result == nil || result.Result.Stdout.Text != "long" {
		t.Fatalf("first lease truncated a renewed command: %+v", result)
	}
	f.run.stop(t)
}
