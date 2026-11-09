package validation

import (
	"golang.org/x/sys/unix"
	"testing"
	"time"
)

func prepareReaper(t *testing.T) {
	t.Helper()
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0) })
}
func waitLeafExited(t *testing.T, pid int) {
	t.Helper()
	end := time.Now().Add(2 * time.Second)
	for time.Now().Before(end) {
		var state unix.WaitStatus
		n, err := unix.Wait4(pid, &state, unix.WNOHANG, nil)
		if err != nil {
			t.Fatal(err)
		}
		if n == pid {
			if !state.Signaled() {
				t.Fatal("leaf did not die from the group signal")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("leaf not reaped")
}
