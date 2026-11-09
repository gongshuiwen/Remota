package validation

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"
)

const roleKey = "REMOTA_VALIDATION_ROLE"
const dirKey = "REMOTA_VALIDATION_DIR"

var clockWindow = flag.Duration("clock-window", 25*time.Millisecond, "observation window for the elapsed clock probe; manual suspend evidence is recorded separately")

// Helpers have fixed roles and a bounded lifetime. They accept no remote command.
func TestMain(m *testing.M) {
	if role := os.Getenv(roleKey); role != "" {
		code := 0
		switch role {
		case "leaf":
			time.Sleep(12 * time.Second)
		case "tree":
			code = treeHelper()
		case "job-owner":
			code = jobOwnerHelper()
		case "sqlite-write":
			code = sqliteWriteHelper()
		case "hold-lock":
			release, err := tryFileLock(filepath.Join(os.Getenv(dirKey), "probe.lock"))
			if err != nil {
				code = 25
			} else {
				if os.WriteFile(filepath.Join(os.Getenv(dirKey), "lock.ready"), []byte("ready"), 0600) != nil {
					code = 26
				} else {
					time.Sleep(12 * time.Second)
				}
				_ = release()
			}
		case "try-lock":
			release, err := tryFileLock(filepath.Join(os.Getenv(dirKey), "probe.lock"))
			if lockBusy(err) {
				code = 23
			} else if err != nil {
				code = 24
			} else {
				_ = release()
			}
		default:
			code = 99
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func helperEnv(role, dir string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
		switch key {
		case "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "HOME", "TMPDIR", "PATH":
			env = append(env, entry)
		}
	}
	return append(env, roleKey+"="+role, dirKey+"="+dir)
}

func helperCommand(role, dir string) *exec.Cmd {
	c := exec.Command(os.Args[0])
	c.Env = helperEnv(role, dir)
	hideHelper(c)
	return c
}

func startHelper(t *testing.T, role, dir string) *exec.Cmd {
	t.Helper()
	c := helperCommand(role, dir)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c.ProcessState == nil {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	})
	return c
}

func waitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper did not become ready")
	return nil
}

func treeHelper() int {
	c := helperCommand("leaf", os.Getenv(dirKey))
	if err := c.Start(); err != nil {
		return 31
	}
	if err := os.WriteFile(filepath.Join(os.Getenv(dirKey), "tree.ready"), []byte(fmt.Sprintf("%d %d", os.Getpid(), c.Process.Pid)), 0600); err != nil {
		_ = c.Process.Kill()
		_ = c.Wait()
		return 32
	}
	_ = c.Wait()
	return 0
}

func TestFileLockAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	release, err := tryFileLock(filepath.Join(dir, "probe.lock"))
	if err != nil {
		t.Fatal(err)
	}
	c := helperCommand("try-lock", dir)
	err = c.Run()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 23 {
		_ = release()
		t.Fatalf("second process acquired lock or failed unexpectedly: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := helperCommand("try-lock", dir).Run(); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	owner := startHelper(t, "hold-lock", dir)
	_ = waitFile(t, filepath.Join(dir, "lock.ready"))
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	if err := helperCommand("try-lock", dir).Run(); err != nil {
		t.Fatalf("lock survived owner crash: %v", err)
	}
}

func TestPlatformElapsedClock(t *testing.T) {
	if *clockWindow <= 0 {
		t.Fatal("clock-window must be positive")
	}
	wallStart := time.Now().UTC()
	a, err := elapsedNow()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("observing %s for %s", clockName(), *clockWindow)
	time.Sleep(*clockWindow)
	b, err := elapsedNow()
	if err != nil {
		t.Fatal(err)
	}
	if b <= a {
		t.Fatalf("elapsed clock did not advance: %v -> %v", a, b)
	}
	if *clockWindow >= time.Second {
		wallElapsed := time.Now().UTC().Sub(wallStart)
		difference := (b - a) - wallElapsed
		if difference < -2*time.Second || difference > 2*time.Second {
			t.Fatalf("elapsed/UTC difference %s exceeds manual probe tolerance; wall clock must remain stable", difference)
		}
		t.Logf("UTC span %s; keep OS suspend/resume evidence to distinguish a suspend test from an awake run", wallElapsed)
	}
	t.Logf("%s: elapsed advanced by %s; physical suspend is a separate native test", clockName(), b-a)
}

func TestNonTerminalInput(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if term.IsTerminal(int(f.Fd())) {
		t.Fatal("non-terminal incorrectly classified as interactive")
	}
}
