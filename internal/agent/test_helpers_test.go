package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"remota.local/remota/internal/clock"
	"remota.local/remota/internal/server"
	"remota.local/remota/internal/server/store"
)

type agentServer struct {
	service *server.Server
	web     *httptest.Server
	handoff store.Handoff
}

func allowTestSpawn(_ *managedProcess, start func() error) string {
	if start() != nil {
		return "SPAWN_FAILED"
	}
	return ""
}

func newAgentServer(t *testing.T, source clock.Source, wrap func(http.Handler) http.Handler) *agentServer {
	t.Helper()
	dir := t.TempDir()
	_, handoff, err := store.Initialize(dir, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewUnstartedServer(nil)
	service, err := server.Open(server.Config{StateDir: dir, Listen: web.Listener.Addr().String(), Clock: source})
	if err != nil {
		web.Close()
		t.Fatal(err)
	}
	handler := service.Handler()
	if wrap != nil {
		handler = wrap(handler)
	}
	web.Config.Handler = handler
	web.Start()
	t.Cleanup(func() { web.Close(); service.Close() })
	return &agentServer{service: service, web: web, handoff: handoff}
}

func replayResponse(w http.ResponseWriter, recorded *httptest.ResponseRecorder) {
	for key, values := range recorded.Header() {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.WriteHeader(recorded.Code)
	_, _ = io.Copy(w, recorded.Body)
}

type agentRun struct {
	input chan string
	done  chan struct{}
	err   error // read only after done closes
}

func startAgentRun(t *testing.T, a *Agent, access string, output io.Writer) *agentRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &agentRun{input: make(chan string, 4), done: make(chan struct{})}
	go func() { run.err = a.Run(ctx, access, run.input, output); close(run.done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(10 * time.Second):
			t.Error("Agent cleanup did not finish")
		}
	})
	return run
}

func (r *agentRun) wait(t *testing.T, condition func() bool) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-r.done:
			t.Fatalf("Agent stopped early: %v", r.err)
		case <-timer.C:
			t.Fatal("timed out waiting for Agent state")
		case <-ticker.C:
		}
	}
}

func (r *agentRun) stop(t *testing.T) {
	t.Helper()
	r.input <- "q"
	select {
	case <-r.done:
		if r.err != nil {
			t.Fatal(r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Agent did not stop")
	}
}
