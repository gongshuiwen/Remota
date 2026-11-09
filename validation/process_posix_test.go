//go:build linux || darwin

package validation

import (
	"fmt"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPOSIXProcessGroupAndParentCrash(t *testing.T) {
	prepareReaper(t)
	for _, cause := range []string{"group-kill", "parent-crash"} {
		t.Run(cause, func(t *testing.T) {
			dir := t.TempDir()
			root := helperCommand("tree", dir)
			root.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := root.Start(); err != nil {
				t.Fatal(err)
			}
			group := root.Process.Pid
			cleaned := false
			t.Cleanup(func() {
				if !cleaned {
					_ = unix.Kill(-group, unix.SIGKILL)
				}
				if root.ProcessState == nil {
					_ = root.Wait()
				}
			})
			var rootPID, leaf int
			if _, err := fmt.Sscanf(string(waitFile(t, filepath.Join(dir, "tree.ready"))), "%d %d", &rootPID, &leaf); err != nil {
				t.Fatal(err)
			}
			if rootPID != group {
				t.Fatal("wrong bootstrap process")
			}
			if pg, err := unix.Getpgid(leaf); err != nil || pg != group {
				t.Fatalf("leaf not in managed group: %d %v", pg, err)
			}
			if cause == "parent-crash" {
				if err := root.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = root.Wait()
				time.Sleep(30 * time.Millisecond)
				if err := unix.Kill(leaf, 0); err != nil {
					t.Fatalf("expected live orphan before explicit cleanup: %v", err)
				}
			}
			if err := unix.Kill(-group, unix.SIGKILL); err != nil {
				t.Fatal(err)
			}
			if root.ProcessState == nil {
				_ = root.Wait()
			}
			waitLeafExited(t, leaf)
			cleaned = true
		})
	}
}
