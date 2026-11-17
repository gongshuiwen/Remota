//go:build linux || darwin

package agent

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"remota.local/remota/internal/clock"
)

func TestProcessOutputEnvironmentAndExit(t *testing.T) {
	t.Setenv("REMOTA_TEST_SECRET", "must-not-be-inherited")
	spec := commandSpec{shell: "sh", command: `printf '%s\n' "${REMOTA_TEST_SECRET-unset}"; if read x; then printf input; else printf eof; fi; printf '\377'; head -c 9000 /dev/zero | tr '\000' x; printf error >&2; exit 7`}
	p, reason := startProcess(spec, allowTestSpawn)
	if p == nil {
		t.Fatalf("spawn failed: %s", reason)
	}
	stop := make(chan time.Duration, 1)
	source, err := clock.NewReal()
	if err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(5*time.Second, func() { stop <- source().Elapsed + time.Second })
	defer timer.Stop()
	r := p.wait(stop, source)
	if !r.confirmed || r.exitCode == nil || *r.exitCode != 7 {
		t.Fatalf("exit not confirmed: %+v", r)
	}
	if !strings.HasPrefix(r.stdout.text, "unset\neof\uFFFD") || r.stdout.capturedBytes != outputLimit || !r.stdout.truncated {
		t.Fatalf("unexpected stdout: bytes=%d truncated=%v prefix=%q", r.stdout.capturedBytes, r.stdout.truncated, r.stdout.text[:min(20, len(r.stdout.text))])
	}
	if r.stderr.text != "error" || r.stderr.capturedBytes != 5 || r.stderr.truncated {
		t.Fatalf("stderr: %+v", r.stderr)
	}
}

func TestProcessCancellationStopsShell(t *testing.T) {
	p, reason := startProcess(commandSpec{shell: "auto", command: "exec sleep 30"}, allowTestSpawn)
	if p == nil {
		t.Fatalf("spawn failed: %s", reason)
	}
	stop := make(chan time.Duration, 1)
	source, err := clock.NewReal()
	if err != nil {
		t.Fatal(err)
	}
	stop <- source().Elapsed + 5*time.Second
	r := p.wait(stop, source)
	if !r.confirmed {
		t.Fatal("killed shell was not reaped and its pipes drained")
	}
}

func TestUnsupportedShellNeverSpawns(t *testing.T) {
	if p, reason := startProcess(commandSpec{shell: "powershell", command: "exit 0"}, allowTestSpawn); p != nil || reason != "UNSUPPORTED_SHELL" {
		t.Fatalf("unsupported shell: %v %q", p, reason)
	}
}

func TestShellExitDoesNotConfirmLiveBackgroundProcess(t *testing.T) {
	p, reason := startProcess(commandSpec{shell: "sh", command: "sleep 3 &"}, allowTestSpawn)
	if p == nil {
		t.Fatalf("spawn failed: %s", reason)
	}
	source, err := clock.NewReal()
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan time.Duration, 1)
	done := make(chan processResult, 1)
	go func() { done <- p.wait(stop, source) }()
	select {
	case result := <-done:
		t.Fatalf("shell exit was mistaken for complete cleanup: %+v", result)
	case <-time.After(150 * time.Millisecond):
	}
	stop <- source().Elapsed + 500*time.Millisecond
	select {
	case result := <-done:
		if result.confirmed && unix.Kill(-p.cmd.Process.Pid, 0) == nil {
			t.Fatal("confirmed while managed group still exists")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not respect its deadline")
	}
}
