//go:build linux

package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"remota.local/remota/internal/auth"
	"remota.local/remota/internal/protocol"
)

// A private controller for Playwright. All business traffic uses product binaries.
func TestHTTPSProducts(t *testing.T) {
	if os.Getenv("REMOTA_HTTPS_TEST") != "1" {
		t.Skip("private HTTPS product acceptance controller")
	}
	output := json.NewEncoder(os.Stdout)
	stage := "startup"
	// Emit a protocol failure before testing prints its diagnostic report.
	t.Cleanup(func() {
		if t.Failed() {
			output.Encode(object{"failure": stage})
		}
	})
	dir := t.TempDir()
	binaries, err := filepath.Abs(os.Getenv("REMOTA_HTTPS_BINARIES"))
	require(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	command := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, filepath.Join(binaries, "remota-server"), args...)
		cmd.Dir, cmd.Env = dir, []string{"PATH=/usr/bin:/bin"}
		return cmd
	}
	state := filepath.Join(dir, "state")
	b, err := command("init", "--state-dir", state).Output()
	require(t, err)
	var bootstrap struct {
		Password string `json:"admin_password"`
	}
	require(t, json.Unmarshal(b, &bootstrap))
	clear(b)
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	require(t, err)
	backendURL, err := url.Parse("http://" + backend.Addr().String())
	require(t, err)
	require(t, backend.Close())
	proxyHandler := httputil.NewSingleHostReverseProxy(backendURL)
	proxyHandler.FlushInterval = -1
	proxyHandler.ErrorLog = log.New(io.Discard, "", 0)
	var polls atomic.Uint64
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/poll") {
			polls.Add(1)
		}
		proxyHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	origin := "https://" + proxy.Listener.Addr().String()
	certificate, ca := httpsCertificate(t)
	proxy.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	proxy.Config.ErrorLog = log.New(io.Discard, "", 0)
	var product *httpsProcess
	t.Cleanup(func() {
		if product != nil {
			select {
			case <-product.done:
				if product.err != nil {
					t.Errorf("product server shutdown failed: %v", product.err)
				}
			default:
				t.Error("product server is still running")
			}
		}
	})
	product = startHTTPSProcess(t, command("serve", "--state-dir", state, "--listen", backendURL.Host, "--public-url", origin))
	proxy.StartTLS()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("invalid test CA")
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	eventually(t, 10*time.Second, func() bool {
		select {
		case <-product.done:
			t.Fatal("product server exited during startup")
		default:
		}
		r, err := client.Get(origin + "/healthz")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	})
	validator, err := protocol.NewValidator()
	require(t, err)
	peer := &httpPeer{client: client, validator: validator, url: origin}
	badTLS := httptest.NewUnstartedServer(http.NotFoundHandler())
	badTLS.Config.ErrorLog = log.New(io.Discard, "", 0)
	badTLS.StartTLS()
	t.Cleanup(badTLS.Close)
	certFile := filepath.Join(dir, "ca.pem")
	require(t, os.WriteFile(certFile, ca, 0600))
	emptyCert := filepath.Join(dir, "untrusted.pem")
	require(t, os.WriteFile(emptyCert, nil, 0600))
	cdp := startHTTPSBrowser(t, ctx, os.Getenv("REMOTA_HTTPS_CHROMIUM"), ca)
	require(t, output.Encode(object{"origin": origin, "password": bootstrap.Password, "cdp": cdp, "untrustedOrigin": badTLS.URL}))
	var agent *httpsAgent
	var mcp *mcpClient
	var marker string
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		var c struct {
			Action, Token, Input, Name, Session, Kind string
			Arguments                                 object
			Trusted                                   bool
		}
		require(t, json.Unmarshal(input.Bytes(), &c))
		stage = c.Action
		var reply any = object{"ok": true}
		switch c.Action {
		case "security":
			untrusted := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()}}}
			_, err := untrusted.Get(origin + "/healthz")
			var unknown x509.UnknownAuthorityError
			if !errors.As(err, &unknown) {
				t.Fatal("MCP-side TLS client accepted an unknown CA")
			}
			r, err := http.NewRequest("GET", origin+"/healthz", nil)
			require(t, err)
			r.Host = "untrusted.example"
			r.Header.Set("X-Forwarded-Host", proxy.Listener.Addr().String())
			r.Header.Set("X-Forwarded-Proto", "https")
			response, err := client.Do(r)
			require(t, err)
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			require(t, err)
			if response.StatusCode != 403 || !bytes.Contains(data, []byte("HOST_DENIED")) {
				t.Fatal("proxy Host boundary failed")
			}
			denied, _ := peer.exchange(t, "POST", "/api/v1/admin/login", object{"password": bootstrap.Password}, http.Header{"Origin": {"https://untrusted.example"}}, 403)
			if denied["error"].(object)["code"] != "ORIGIN_DENIED" {
				t.Fatal("proxy Origin boundary failed")
			}
		case "connect":
			mcp = newMCPClient(t, peer, c.Token)
			v, _ := mcp.rpc(t, "tools/list", object{}, "MCPToolsListResponse")
			if len(v["result"].(object)["tools"].([]any)) != 8 {
				t.Fatal("HTTPS MCP discovery did not return eight tools")
			}
		case "agent":
			cert := emptyCert
			if c.Trusted {
				cert = certFile
			}
			agent = startHTTPSAgent(t, ctx, filepath.Join(binaries, "remota-agent"), origin, c.Token, cert)
			if c.Trusted {
				agent.prompt(t, "Press Enter to accept")
			} else {
				select {
				case <-agent.process.done:
				case <-time.After(10 * time.Second):
					t.Fatal("untrusted Agent did not stop")
				}
				if agent.process.err == nil {
					t.Fatal("product Agent accepted an unknown CA")
				}
				eventually(t, time.Second, func() bool {
					return strings.Contains(agent.text.String(), "x509: certificate signed by unknown authority")
				})
			}
		case "local":
			agent.send(t, c.Input)
			if c.Input == "f" {
				agent.prompt(t, "Type full to confirm")
			}
		case "tool":
			reply = mcp.ok(t, c.Name, c.Arguments)
		case "revoked":
			v, h := peer.exchange(t, "POST", "/mcp", object{"jsonrpc": "2.0", "id": "revoked-probe", "method": "tools/list", "params": object{}}, mcp.headers(), 401)
			if v["error"].(object)["code"] != "UNAUTHENTICATED" || h.Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("rotated MCP credential remained usable")
			}
		case "command":
			text, seconds := "printf 'tls-out'; printf 'tls-err' >&2", 10
			if c.Kind == "long" {
				marker = filepath.Join(dir, auth.ID("processes"))
				file := marker
				t.Cleanup(func() { cleanupHTTPSCommand(file) })
				q := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
				text, seconds = "echo $$ > "+q+"; bash -c 'exec -a \"$0\" sleep 70' "+q+" & echo $! >> "+q+"; wait", 80
			}
			reply = mcp.ok(t, "request_command", object{"session_id": c.Session, "request_id": auth.ID(""), "command": text, "reason": "HTTPS acceptance", "shell": "sh", "timeout_seconds": seconds})
			if c.Kind == "long" {
				tid := reply.(object)["task"].(object)["id"].(string)
				started := false
				t.Cleanup(func() {
					if !started {
						task := mcp.task(t, c.Session, tid)
						b, _ := os.ReadFile(marker)
						t.Logf("command start observation: phase=%v execution=%v outcome=%v error_code=%v stop=%v marker_fields=%d polls=%d", task["phase"], task["execution"], task["outcome"], task["error_code"], task["stop"], len(strings.Fields(string(b))), polls.Load())
					}
				})
				eventually(t, 8*time.Second, func() bool {
					b, _ := os.ReadFile(marker)
					return len(strings.Fields(string(b))) == 2 && mcp.task(t, c.Session, tid)["execution"].(object)["state"] == "started"
				})
				started = true
			}
		case "observe":
			exited := false
			select {
			case <-agent.process.done:
				exited = true
			default:
			}
			reply = object{"polls": polls.Load(), "processes_stopped": httpsCommandStopped(marker), "agent_exited": exited}
		default:
			t.Fatal("unknown HTTPS test action")
		}
		require(t, output.Encode(reply))
	}
	require(t, input.Err())
	stage = "shutdown"
}

func httpsCommandStopped(marker string) bool {
	b, err := os.ReadFile(marker)
	if err != nil || len(strings.Fields(string(b))) != 2 {
		return false
	}
	for _, text := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(text)
		if err != nil {
			return false
		}
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !strings.HasPrefix(string(stat[strings.LastIndex(string(stat), ")")+1:]), " Z") {
			return false
		}
	}
	return true
}

func cleanupHTTPSCommand(marker string) {
	b, _ := os.ReadFile(marker)
	ids := strings.Fields(string(b))
	if len(ids) != 2 {
		return
	}
	parent, err := strconv.Atoi(ids[0])
	if err != nil || parent <= 1 {
		return
	}
	for _, text := range ids {
		pid, err := strconv.Atoi(text)
		if err != nil {
			continue
		}
		args, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		group, groupErr := syscall.Getpgid(pid)
		if err == nil && groupErr == nil && group == parent && bytes.Contains(args, []byte(marker)) {
			syscall.Kill(-group, syscall.SIGKILL)
			return
		}
	}
}
