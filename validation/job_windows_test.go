package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The suspended process is a trusted bootstrap. It creates the leaf only after
// assignment to the Job and ResumeThread, proving the boundary-before-command path.
func jobTree(dir string) (windows.Handle, windows.ProcessInformation, error) {
	var pi windows.ProcessInformation
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, pi, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, pi, err
	}
	app, err := windows.UTF16PtrFromString(os.Args[0])
	if err != nil {
		windows.CloseHandle(job)
		return 0, pi, err
	}
	command, err := windows.UTF16PtrFromString(syscall.EscapeArg(os.Args[0]))
	if err != nil {
		windows.CloseHandle(job)
		return 0, pi, err
	}
	env := helperEnv("tree", dir)
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	if err := windows.CreateProcess(app, command, nil, nil, false, windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW, &block[0], nil, &si, &pi); err != nil {
		windows.CloseHandle(job)
		return 0, pi, err
	}
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(job)
		return 0, windows.ProcessInformation{}, err
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		windows.CloseHandle(job)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		return 0, windows.ProcessInformation{}, err
	}
	windows.CloseHandle(pi.Thread)
	pi.Thread = 0
	return job, pi, nil
}

func jobOwnerHelper() int {
	job, pi, err := jobTree(os.Getenv(dirKey))
	if err != nil {
		return 51
	}
	defer windows.CloseHandle(job)
	defer windows.CloseHandle(pi.Process)
	time.Sleep(12 * time.Second)
	return 0
}

func windowsOwnedHandle(t *testing.T, pid int) windows.Handle {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.TerminateProcess(h, 1); _ = windows.CloseHandle(h) })
	return h
}
func windowsAssertExited(t *testing.T, h windows.Handle) {
	t.Helper()
	status, err := windows.WaitForSingleObject(h, 2000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned process remained alive: %d %v", status, err)
	}
}

func windowsAssertInJob(t *testing.T, process, job windows.Handle) {
	t.Helper()
	var member uint32
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
	ok, _, err := proc.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&member)))
	if ok == 0 || member == 0 {
		t.Fatalf("process is not in the managed Job: %v", err)
	}
}

func TestWindowsJobClosesWholeTree(t *testing.T) {
	dir := t.TempDir()
	job, pi, err := jobTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if job != 0 {
			windows.CloseHandle(job)
		}
		windows.CloseHandle(pi.Process)
	})
	var root, leaf int
	if _, err := fmt.Sscanf(string(waitFile(t, filepath.Join(dir, "tree.ready"))), "%d %d", &root, &leaf); err != nil {
		t.Fatal(err)
	}
	if root != int(pi.ProcessId) {
		t.Fatal("wrong bootstrap process")
	}
	leafHandle := windowsOwnedHandle(t, leaf)
	windowsAssertInJob(t, pi.Process, job)
	windowsAssertInJob(t, leafHandle, job)
	var info struct {
		UserTime, KernelTime, PeriodUserTime, PeriodKernelTime int64
		Faults, Total, Active, Terminated                      uint32
	}
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil || info.Active < 2 {
		t.Fatalf("Job membership: %d %v", info.Active, err)
	}
	if err := windows.CloseHandle(job); err != nil {
		t.Fatal(err)
	}
	job = 0
	windowsAssertExited(t, pi.Process)
	windowsAssertExited(t, leafHandle)
}

func TestWindowsJobOwnerCrash(t *testing.T) {
	dir := t.TempDir()
	owner := startHelper(t, "job-owner", dir)
	var root, leaf int
	if _, err := fmt.Sscanf(string(waitFile(t, filepath.Join(dir, "tree.ready"))), "%d %d", &root, &leaf); err != nil {
		t.Fatal(err)
	}
	rootHandle, leafHandle := windowsOwnedHandle(t, root), windowsOwnedHandle(t, leaf)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	windowsAssertExited(t, rootHandle)
	windowsAssertExited(t, leafHandle)
}
