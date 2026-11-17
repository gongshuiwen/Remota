package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"remota.local/remota/internal/protocol"
)

type linuxServer struct {
	peer   *httpPeer
	info   serverReply
	input  *json.Encoder
	output *json.Decoder
}

func (s *linuxServer) control(t *testing.T, command serverCommand) serverReply {
	t.Helper()
	require(t, s.input.Encode(command))
	var reply serverReply
	require(t, s.output.Decode(&reply))
	return reply
}

func startLinuxServer(t *testing.T) (*linuxServer, string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require(t, err)
	bin := t.TempDir()
	agent, helper := filepath.Join(bin, "remota-agent.exe"), filepath.Join(bin, "server.test")
	build := func(args []string, env []string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir, cmd.Env = root, append(os.Environ(), env...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build failed: %v\n%s", err, b)
		}
	}
	build([]string{"build", "-mod=readonly", "-o", agent, "./cmd/remota-agent"}, []string{"CGO_ENABLED=0"})
	build([]string{"test", "-mod=readonly", "-c", "-o", helper, "./tests/integration"}, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + runtime.GOARCH})
	wslArgs := []string{}
	if distro := os.Getenv("REMOTA_E2E_WSL_DISTRO"); distro != "" {
		wslArgs = append(wslArgs, "--distribution", distro)
	}
	convert := exec.Command("wsl.exe", append(append([]string{}, wslArgs...), "--exec", "wslpath", "-u", helper)...)
	path, err := convert.Output()
	require(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	cmd := exec.CommandContext(ctx, "wsl.exe", append(wslArgs, "--exec", "env", "REMOTA_WINDOWS_E2E_HELPER=1", "TMPDIR=/tmp", strings.TrimSpace(string(path)), "-test.run=^TestWindowsAgentServer$", "-test.timeout=4m")...)
	stdin, err := cmd.StdinPipe()
	require(t, err)
	stdout, err := cmd.StdoutPipe()
	require(t, err)
	var stderr terminalText
	cmd.Stderr = &stderr
	require(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		stdin.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Linux helper failed: %v\n%s", err, stderr.String())
			}
		case <-time.After(10 * time.Second):
			cancel()
			<-done
			t.Error("Linux helper did not shut down")
		}
		cancel()
	})
	s := &linuxServer{input: json.NewEncoder(stdin), output: json.NewDecoder(stdout)}
	require(t, s.output.Decode(&s.info))
	v, err := protocol.NewValidator()
	require(t, err)
	s.peer = &httpPeer{url: s.info.URL, client: &http.Client{Timeout: 10 * time.Second}, validator: v}
	t.Cleanup(s.peer.client.CloseIdleConnections)
	return s, agent
}

func fullTerminal(t *testing.T, s *linuxServer, executable string, m *mcpClient) (*agentTerminal, string) {
	t.Helper()
	known := make(map[string]bool)
	for _, value := range m.ok(t, "list_sessions", object{})["sessions"].([]any) {
		known[value.(object)["id"].(string)] = true
	}
	a := startTerminal(t, executable, s.info.URL, s.info.Access)
	var sid string
	for _, value := range m.ok(t, "list_sessions", object{})["sessions"].([]any) {
		session := value.(object)
		if id := session["id"].(string); !known[id] {
			if sid != "" || session["state"] != "pending" {
				t.Fatal("expected one new pending local confirmation")
			}
			sid = id
		}
	}
	if sid == "" {
		t.Fatal("new Agent session not found")
	}
	a.send(t, "f")
	a.prompt(t, "Type full to confirm")
	if m.ok(t, "get_session", object{"session_id": sid})["session"].(object)["state"] != "pending" {
		t.Fatal("full access was granted before the second local confirmation")
	}
	a.send(t, "full")
	eventually(t, 12*time.Second, func() bool {
		v := m.ok(t, "get_session", object{"session_id": sid})["session"].(object)
		return v["state"] == "active" && v["consent"].(object)["mode"] == "full" && v["connection"].(object)["activity"] == "recent"
	})
	return a, sid
}

func requestCommand(t *testing.T, m *mcpClient, sid, script string, seconds int) (string, object) {
	t.Helper()
	args := object{"session_id": sid, "request_id": strings.Repeat("1", 32), "command": script, "reason": "Windows end-to-end acceptance", "shell": "powershell", "timeout_seconds": seconds}
	data := m.ok(t, "request_command", args)
	if data["accepted"] != true || data["reused"] != false {
		t.Fatal("command was not accepted as a new intent")
	}
	return data["task"].(object)["id"].(string), args
}

func TestWindowsMCPAgent(t *testing.T) {
	if os.Getenv("REMOTA_WINDOWS_E2E") != "1" {
		t.Skip("set REMOTA_WINDOWS_E2E=1 to run native Agent + WSL/Linux server acceptance")
	}
	s, executable := startLinuxServer(t)
	t.Run("result_loss_idempotency_and_output", func(t *testing.T) {
		m := newMCPClient(t, s.peer, s.info.MCP)
		a, sid := fullTerminal(t, s, executable, m)
		info := m.ok(t, "get_agent_info", object{"session_id": sid})
		if info["os"] != "windows" || info["arch"] != runtime.GOARCH {
			t.Fatal("diagnostic did not come from the native Windows Agent")
		}
		marker := filepath.Join(t.TempDir(), "spawned")
		s.control(t, serverCommand{Fault: "result_reply_lost"})
		script := fmt.Sprintf("[IO.File]::AppendAllText(%s, 'x'); [Console]::Out.Write('成功'); [Console]::Out.Write(('x' * 9000)); [Console]::Error.Write(('e' * 9000))", quotePowerShell(marker))
		tid, args := requestCommand(t, m, sid, script, 20)
		reused := m.ok(t, "request_command", args)
		if reused["reused"] != true || reused["task"].(object)["id"] != tid {
			t.Fatal("MCP intent retry created another task")
		}
		eventually(t, 15*time.Second, func() bool {
			counts := s.control(t, serverCommand{TaskID: tid})
			return m.task(t, sid, tid)["phase"] == "settled" && counts.Results >= 2 && counts.LostReplies == 1
		})
		result := m.task(t, sid, tid)
		output := result["result"].(object)
		for stream, text := range map[string]string{"stdout": "成功" + strings.Repeat("x", 8192-6), "stderr": strings.Repeat("e", 8192)} {
			v := output[stream].(object)
			if v["text"] != text || protocol.Integer(v["captured_bytes"]) != 8192 || v["truncated"] != true {
				t.Fatalf("%s capture is not bounded and independent", stream)
			}
		}
		if result["outcome"] != "succeeded" || result["execution"].(object)["state"] != "exited" || protocol.Integer(result["execution"].(object)["exit_code"]) != 0 {
			t.Fatal("full result did not confirm successful exit")
		}
		content, err := os.ReadFile(marker)
		require(t, err)
		if string(content) != "x" || s.control(t, serverCommand{TaskID: tid}).Starts != 1 {
			t.Fatal("command was started more than once")
		}
		a.finish(t)
	})
	t.Run("lost_start_reply", func(t *testing.T) {
		m := newMCPClient(t, s.peer, s.info.MCP)
		a, sid := fullTerminal(t, s, executable, m)
		marker := filepath.Join(t.TempDir(), "must-not-exist")
		s.control(t, serverCommand{Fault: "start_reply_lost"})
		tid, _ := requestCommand(t, m, sid, "[IO.File]::WriteAllText("+quotePowerShell(marker)+", 'spawned')", 20)
		eventually(t, 12*time.Second, func() bool { return m.task(t, sid, tid)["phase"] == "settled" })
		v, counts := m.task(t, sid, tid), s.control(t, serverCommand{TaskID: tid})
		if v["execution"].(object)["state"] != "not_started" || v["error_code"] != "GRANT_UNAVAILABLE" || counts.Starts != 1 || counts.Results != 0 || counts.LostReplies != 1 {
			t.Fatal("lost start reply was retried or mistaken for execution")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("command executed without an observed start grant")
		}
		a.finish(t)
	})
	for _, test := range []struct {
		name, resultOutcome, mode string
		seconds, pollFailures     int
	}{
		{"operator_cancel", "cancelled", "full", 30, 0},
		{"timeout", "timed_out", "full", 4, 0},
		{"mcp_client_ended", "", "full", 30, 0},
		{"credential_revoked", "", "full", 30, 0},
		{"readonly", "", "readonly", 30, 0},
		{"transport_lost", "", "full", 30, 1},
		{"local_end", "", "", 30, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := s.control(t, serverCommand{Fault: "reset"})
			observer := newMCPClient(t, s.peer, s.info.MCP)
			actor := observer
			var revoke func()
			if test.name == "mcp_client_ended" {
				actor = newMCPClient(t, s.peer, s.info.MCP)
			}
			if test.name == "credential_revoked" {
				actor, revoke = revocableClient(t, s)
			}
			a, sid := fullTerminal(t, s, executable, observer)
			marker := filepath.Join(t.TempDir(), "processes")
			script := "$p = Start-Process -WindowStyle Hidden -FilePath $env:ComSpec -ArgumentList '/c ping -n 60 127.0.0.1 >NUL' -PassThru; " +
				"[IO.File]::WriteAllText(" + quotePowerShell(marker) + ", \"$PID,$($p.Id)`n\"); [Console]::Out.Write('private'); Start-Sleep -Seconds 60"
			tid, _ := requestCommand(t, actor, sid, script, test.seconds)
			eventually(t, 10*time.Second, func() bool {
				return observer.task(t, sid, tid)["execution"].(object)["state"] == "started"
			})
			processes := observeProcesses(t, marker)
			switch test.name {
			case "operator_cancel":
				actor.ok(t, "cancel_task", object{"session_id": sid, "task_id": tid})
			case "mcp_client_ended":
				s.peer.exchange(t, "DELETE", "/mcp", nil, actor.headers(), 204)
			case "credential_revoked":
				revoke()
			case "readonly":
				a.send(t, "r")
			case "transport_lost":
				s.control(t, serverCommand{Fault: "poll_failure"})
				actor.ok(t, "ping_agent", object{"session_id": sid})
			case "local_end":
				a.finish(t)
			}
			eventually(t, 12*time.Second, func() bool { return observer.task(t, sid, tid)["phase"] == "settled" })
			v, counts := observer.task(t, sid, tid), s.control(t, serverCommand{TaskID: tid})
			if v["execution"].(object)["state"] != "exited" || counts.Starts != 1 {
				t.Fatal("task did not confirm one managed execution and exit")
			}
			for _, process := range processes {
				if status, err := windows.WaitForSingleObject(process, 0); err != nil || status != windows.WAIT_OBJECT_0 {
					t.Fatal("managed Windows process survived its exit receipt")
				}
			}
			if test.resultOutcome != "" {
				if v["outcome"] != test.resultOutcome || v["result"] == nil || counts.Results < 1 {
					t.Fatal("ordinary stop lost its complete result")
				}
			} else if v["result"] != nil || counts.Results != 0 {
				t.Fatal("output was reported after local or remote authorization loss")
			}
			if counts.PollFailures-before.PollFailures != test.pollFailures {
				t.Fatal("unexpected number of injected poll failures")
			}
			if test.mode != "" {
				session := observer.ok(t, "get_session", object{"session_id": sid})["session"].(object)
				if session["state"] != "active" || session["consent"].(object)["mode"] != test.mode {
					t.Fatal("task stop incorrectly ended or elevated the device session")
				}
				observer.ok(t, "ping_agent", object{"session_id": sid})
				a.finish(t)
			} else if v["details_available"] != false || v["spec"] != nil {
				t.Fatal("ended session retained sensitive task details")
			}
		})
	}
	t.Run("lease_renewal", func(t *testing.T) {
		m := newMCPClient(t, s.peer, s.info.MCP)
		a, sid := fullTerminal(t, s, executable, m)
		tid, _ := requestCommand(t, m, sid, "Start-Sleep -Seconds 33; [Console]::Out.Write('renewed')", 45)
		eventuallyEvery(t, 45*time.Second, 500*time.Millisecond, func() bool { return m.task(t, sid, tid)["phase"] == "settled" })
		v := m.task(t, sid, tid)
		if v["outcome"] != "succeeded" || v["result"].(object)["stdout"].(object)["text"] != "renewed" || s.control(t, serverCommand{TaskID: tid}).Starts != 1 {
			t.Fatal("the first lease incorrectly truncated a renewing command")
		}
		a.finish(t)
	})
}

func revocableClient(t *testing.T, s *linuxServer) (*mcpClient, func()) {
	t.Helper()
	headers := http.Header{"Origin": {s.info.URL}}
	v, h := s.peer.exchange(t, "POST", "/api/v1/admin/login", object{"password": s.info.Password}, headers, 200)
	for _, cookie := range (&http.Response{Header: h}).Cookies() {
		if cookie.Name == "remota_admin" {
			headers.Set("Cookie", cookie.Name+"="+cookie.Value)
		}
	}
	headers.Set("X-CSRF-Token", v["csrf_token"].(string))
	issue, _ := s.peer.exchange(t, "POST", "/api/v1/admin/credentials", object{"operation_id": strings.Repeat("2", 32), "kind": "mcp", "name": "Windows revocation acceptance", "allow_exec": true}, headers, 201)
	id := issue["credential"].(object)["id"].(string)
	return newMCPClient(t, s.peer, issue["secret"].(string)), func() {
		s.peer.exchange(t, "POST", "/api/v1/admin/credentials/"+id+"/revoke", object{}, headers, 200)
	}
}

func observeProcesses(t *testing.T, marker string) []windows.Handle {
	t.Helper()
	var pids [2]uint32
	eventually(t, 10*time.Second, func() bool {
		b, err := os.ReadFile(marker)
		line, complete := strings.CutSuffix(string(b), "\n")
		if err != nil || !complete {
			return false
		}
		ids := strings.Split(line, ",")
		if len(ids) != len(pids) {
			return false
		}
		for i, id := range ids {
			pid, err := strconv.ParseUint(id, 10, 32)
			if err != nil || pid == 0 {
				return false
			}
			pids[i] = uint32(pid)
		}
		return true
	})
	var handles []windows.Handle
	for _, pid := range pids {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		require(t, err)
		t.Cleanup(func() { windows.CloseHandle(handle) })
		if status, err := windows.WaitForSingleObject(handle, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
			t.Fatal("process was not alive before cancellation")
		}
		handles = append(handles, handle)
	}
	return handles
}
