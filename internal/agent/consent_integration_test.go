package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type reductionNotice struct {
	once  sync.Once
	ready chan struct{}
}

func (w *reductionNotice) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "Read-only mode requested.") {
		w.once.Do(func() { close(w.ready) })
	}
	return len(p), nil
}

func TestReductionWhileFullDecisionResponseIsPending(t *testing.T) {
	decided, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	backend := newAgentServer(t, nil, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/decision") {
				recorded := httptest.NewRecorder()
				next.ServeHTTP(recorded, r)
				close(decided)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				replayResponse(w, recorded)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	a, err := New(backend.web.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	output := &reductionNotice{ready: make(chan struct{})}
	run := startAgentRun(t, a, backend.handoff.Access, output)
	run.input <- "f"
	run.input <- "full"
	select {
	case <-decided:
	case <-time.After(5 * time.Second):
		t.Fatal("decision was not submitted")
	}
	run.input <- "r"
	select {
	case <-output.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("local reduction waited for the server response")
	}
	releaseOnce.Do(func() { close(release) })
	run.wait(t, func() bool {
		v := backend.service.Core.Snapshot()
		return len(v.Sessions) == 1 && v.Sessions[0].State == "active" && v.Sessions[0].Consent.Mode == "readonly" && v.Sessions[0].Consent.Revision == 2 && v.Sessions[0].Activity == "recent"
	})
	run.stop(t)
}
