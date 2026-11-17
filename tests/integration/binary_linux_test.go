//go:build linux

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBinaryStartup(t *testing.T) {
	artifacts := os.Getenv("REMOTA_BINARY_DIR")
	if artifacts == "" {
		t.Skip("set REMOTA_BINARY_DIR to the native Linux build output")
	}
	dir := t.TempDir()
	artifacts, err := filepath.Abs(artifacts)
	require(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	command := func(name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, filepath.Join(artifacts, name), args...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH="}
		return cmd
	}
	require(t, command("remota-agent", "--help").Run())
	state := filepath.Join(dir, "state")
	bootstrap, err := command("remota-server", "init", "--state-dir", state).Output()
	require(t, err)
	var handoff struct {
		Password string `json:"admin_password"`
	}
	require(t, json.Unmarshal(bootstrap, &handoff))
	clear(bootstrap)
	if handoff.Password == "" {
		t.Fatal("initialization did not hand off an administrator password")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require(t, err)
	address := listener.Addr().String()
	require(t, listener.Close())
	base := "http://" + address
	child := command("remota-server", "serve", "--state-dir", state, "--listen", address)
	require(t, child.Start())
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	t.Cleanup(func() {
		child.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("binary did not shut down cleanly: %v", err)
			}
		case <-time.After(5 * time.Second):
			child.Process.Kill()
			<-done
			t.Error("binary shutdown timed out")
		}
	})
	client := &http.Client{Timeout: time.Second}
	eventually(t, 8*time.Second, func() bool {
		r, err := client.Get(base + "/healthz")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	})
	get := func(path string) []byte {
		t.Helper()
		r, err := client.Get(base + path)
		require(t, err)
		defer r.Body.Close()
		if r.StatusCode != 200 || len(r.Cookies()) != 0 {
			t.Fatalf("embedded resource request failed or created an identity: %s (%d)", path, r.StatusCode)
		}
		data, err := io.ReadAll(r.Body)
		require(t, err)
		if len(data) == 0 {
			t.Fatalf("empty embedded resource: %s", path)
		}
		return data
	}
	index := get("/")
	if !bytes.Contains(index, []byte(`<div id="root">`)) {
		t.Fatal("binary does not serve the built console")
	}
	assets := regexp.MustCompile(`(?:src|href)="(/assets/[^"?]+)"`).FindAllSubmatch(index, -1)
	hasScript, hasStyle := false, false
	for _, asset := range assets {
		path := string(asset[1])
		hasScript = hasScript || strings.HasSuffix(path, ".js")
		hasStyle = hasStyle || strings.HasSuffix(path, ".css")
		get(path)
	}
	if !hasScript || !hasStyle {
		t.Fatal("binary console must reference both JavaScript and CSS")
	}
	body, err := json.Marshal(map[string]string{"password": handoff.Password})
	require(t, err)
	r, err := http.NewRequest("POST", base+"/api/v1/admin/login", bytes.NewReader(body))
	require(t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", base)
	login, err := client.Do(r)
	require(t, err)
	defer login.Body.Close()
	if login.StatusCode != 200 || len(login.Cookies()) != 1 || !strings.HasPrefix(login.Header.Get("Content-Type"), "application/json") {
		t.Fatal("binary administrator login failed")
	}
}
