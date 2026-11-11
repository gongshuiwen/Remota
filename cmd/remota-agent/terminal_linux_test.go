//go:build linux

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"remota.local/remota/internal/server"
	"remota.local/remota/internal/server/store"
)

type terminalOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *terminalOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}
func (o *terminalOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

func TestTerminalReadonlyAgent(t *testing.T) {
	if address := os.Getenv("REMOTA_AGENT_TEST_URL"); address != "" {
		flag.CommandLine = flag.NewFlagSet("remota-agent", flag.ContinueOnError)
		os.Args = []string{"remota-agent", "--server", address}
		if err := run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	_, handoff, err := store.Initialize(dir, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewUnstartedServer(nil)
	service, err := server.Open(server.Config{StateDir: dir, Listen: web.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	web.Config.Handler = service.Handler()
	web.Start()
	t.Cleanup(func() { web.Close(); service.Close() })
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "pty-master")
	defer master.Close()
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalReadonlyAgent$")
	cmd.Env = append(os.Environ(), "REMOTA_AGENT_TEST_URL="+web.URL)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err = cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	slave.Close()
	var output terminalOutput
	readDone := make(chan struct{})
	go func() { io.Copy(&output, master); close(readDone) }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	finished := false
	t.Cleanup(func() {
		cancel()
		if !finished {
			<-done
		}
	})
	wait := func(condition func() bool) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			if condition() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("terminal did not reach expected state: %s", strings.ReplaceAll(output.String(), handoff.Access, "[redacted]"))
	}
	wait(func() bool {
		settings, err := unix.IoctlGetTermios(fd, unix.TCGETS)
		return strings.Contains(output.String(), "Access credential:") && err == nil && settings.Lflag&unix.ECHO == 0
	})
	if _, err = fmt.Fprintln(master, handoff.Access); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return strings.Contains(output.String(), "Press Enter to accept") })
	if _, err = fmt.Fprintln(master); err != nil {
		t.Fatal(err)
	}
	var sid string
	wait(func() bool {
		v := service.Core.Snapshot()
		if len(v.Sessions) != 1 || v.Sessions[0].State != "active" || v.Sessions[0].Activity != "recent" {
			return false
		}
		sid = v.Sessions[0].ID
		return v.Sessions[0].Consent.Mode == "readonly"
	})
	if _, err = fmt.Fprintln(master, "q"); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		finished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("terminal agent did not end")
	}
	<-readDone
	if strings.Contains(output.String(), handoff.Access) {
		t.Fatal("access credential was echoed to the terminal")
	}
	session, err := service.Core.GetSession(sid)
	if err != nil || session.State != "ended" || session.EndReason != "local_end" {
		t.Fatalf("unexpected session after terminal end: %v", err)
	}
}
