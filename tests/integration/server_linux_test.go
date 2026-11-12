package integration

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"remota.local/remota/internal/server"
	"remota.local/remota/internal/server/store"
)

// Only the Windows acceptance test launches this helper. Product HTTP, MCP,
// persistence and clocks are used unchanged; faults affect transport replies.
func TestWindowsAgentServer(t *testing.T) {
	if os.Getenv("REMOTA_WINDOWS_E2E_HELPER") != "1" {
		t.Skip("private Linux server helper for Windows end-to-end acceptance")
	}
	dir := t.TempDir()
	_, handoff, err := store.Initialize(dir, time.Now().UTC())
	require(t, err)
	web := httptest.NewUnstartedServer(nil)
	service, err := server.Open(server.Config{StateDir: dir, Listen: web.Listener.Addr().String()})
	require(t, err)
	defer service.Close()
	defer web.Close()
	var mu sync.Mutex
	fault := ""
	requests := make(map[string]requestCounts)
	pollFailures := 0
	next := service.Handler()
	web.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		kind, id := "", ""
		if len(parts) >= 3 && parts[len(parts)-3] == "tasks" {
			kind, id = parts[len(parts)-1], parts[len(parts)-2]
		}
		mu.Lock()
		counts := requests[id]
		if kind == "start" {
			counts.Starts++
		}
		if kind == "result" {
			counts.Results++
		}
		if id != "" {
			requests[id] = counts
		}
		lose := (fault == "start_reply_lost" && kind == "start") || (fault == "result_reply_lost" && kind == "result")
		drop := fault == "poll_failure" && strings.HasSuffix(r.URL.Path, "/poll")
		if lose || drop {
			fault = ""
		}
		if drop {
			pollFailures++
		}
		mu.Unlock()
		if drop {
			w.WriteHeader(503)
			return
		}
		if lose {
			recorded := httptest.NewRecorder()
			next.ServeHTTP(recorded, r)
			if recorded.Code != http.StatusOK {
				t.Errorf("cannot lose a successful %s reply: upstream HTTP %d", kind, recorded.Code)
			} else {
				mu.Lock()
				counts := requests[id]
				counts.LostReplies++
				requests[id] = counts
				mu.Unlock()
			}
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(503)
			return
		}
		next.ServeHTTP(w, r)
	})
	web.Start()
	output := json.NewEncoder(os.Stdout)
	require(t, output.Encode(serverReply{URL: web.URL, Access: handoff.Access, MCP: handoff.MCP, Password: handoff.AdminPassword}))
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		var command serverCommand
		require(t, json.Unmarshal(input.Bytes(), &command))
		mu.Lock()
		if command.Fault != "" {
			fault = command.Fault
		}
		reply := serverReply{requestCounts: requests[command.TaskID], PollFailures: pollFailures}
		mu.Unlock()
		require(t, output.Encode(reply))
	}
	require(t, input.Err())
}
