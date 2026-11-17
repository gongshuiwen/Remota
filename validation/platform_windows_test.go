package validation

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func hideHelper(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
func clockName() string { return "QueryInterruptTimePrecise (includes suspend bias)" }
func elapsedNow() (time.Duration, error) {
	p := windows.NewLazySystemDLL("api-ms-win-core-realtime-l1-1-1.dll").NewProc("QueryInterruptTimePrecise")
	if err := p.Find(); err != nil {
		return 0, err
	}
	var ticks uint64
	p.Call(uintptr(unsafe.Pointer(&ticks)))
	if ticks > uint64((1<<63-1)/100) {
		return 0, fmt.Errorf("interrupt time overflow")
	}
	return time.Duration(ticks * 100), nil
}

func tryFileLock(path string) (func() error, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	o := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, o); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() error {
		err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, o)
		closeErr := f.Close()
		return errors.Join(err, closeErr)
	}, nil
}
func lockBusy(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
