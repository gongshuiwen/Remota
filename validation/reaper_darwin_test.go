package validation

import (
	"errors"
	"golang.org/x/sys/unix"
	"testing"
	"time"
)

func prepareReaper(t *testing.T) {}
func waitLeafExited(t *testing.T, pid int) {
	t.Helper()
	end := time.Now().Add(2 * time.Second)
	for time.Now().Before(end) {
		if errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("leaf remains observable; native macOS runner must confirm reaping behavior")
}
