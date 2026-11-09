//go:build linux || darwin

package validation

import (
	"errors"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

func hideHelper(c *exec.Cmd) {}
func jobOwnerHelper() int    { return 98 } // Windows-only helper must never be selected here.
func tryFileLock(path string) (func() error, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() error {
		err := unix.Flock(int(f.Fd()), unix.LOCK_UN)
		closeErr := f.Close()
		return errors.Join(err, closeErr)
	}, nil
}
func lockBusy(err error) bool { return errors.Is(err, unix.EWOULDBLOCK) }
