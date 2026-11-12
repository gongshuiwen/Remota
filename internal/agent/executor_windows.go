package agent

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"remota.local/remota/internal/clock"
)

// JOB_LIST assigns the process during CreateProcess, before it can execute.
const procThreadAttributeJobList = 0x0002000d

type managedProcess struct {
	job, process           windows.Handle
	stdout, stderr         *os.File
	stdoutData, stderrData limitedOutput
	out, errOut            chan capturedOutput
}

func (p *managedProcess) discard() { p.stdoutData.discard(); p.stderrData.discard() }

func supportedShells() []string {
	system, err := windows.GetSystemDirectory()
	if err == nil {
		if _, err = os.Stat(filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")); err == nil {
			return []string{"powershell"}
		}
	}
	return []string{}
}

func startProcess(spec commandSpec, gate spawnGate) (*managedProcess, string) {
	if spec.shell != "auto" && spec.shell != "powershell" {
		return nil, "UNSUPPORTED_SHELL"
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, "UNSUPPORTED_SHELL"
	}
	shell := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err = os.Stat(shell); err != nil {
		return nil, "UNSUPPORTED_SHELL"
	}
	home, err := commandHome()
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	keepJob := false
	defer func() {
		if !keepJob {
			windows.CloseHandle(job)
		}
	}()
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, "SPAWN_FAILED"
	}

	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	pipe := func() (*os.File, windows.Handle, error) {
		var read, write windows.Handle
		if err := windows.CreatePipe(&read, &write, &sa, 0); err != nil {
			return nil, 0, err
		}
		if err := windows.SetHandleInformation(read, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			windows.CloseHandle(read)
			windows.CloseHandle(write)
			return nil, 0, err
		}
		return os.NewFile(uintptr(read), "command-output"), write, nil
	}
	out, outWrite, err := pipe()
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	keepOut := false
	defer func() {
		windows.CloseHandle(outWrite)
		if !keepOut {
			out.Close()
		}
	}()
	errOut, errWrite, err := pipe()
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	defer func() {
		windows.CloseHandle(errWrite)
		if !keepOut {
			errOut.Close()
		}
	}()
	nul, _ := windows.UTF16PtrFromString("NUL")
	stdin, err := windows.CreateFile(nul, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	defer windows.CloseHandle(stdin)
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	defer attributes.Delete()
	handles := []windows.Handle{stdin, outWrite, errWrite}
	if attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&job), unsafe.Sizeof(job)) != nil ||
		attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])) != nil {
		return nil, "SPAWN_FAILED"
	}
	setup := "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); $OutputEncoding = [Console]::OutputEncoding;\n"
	command := base64.StdEncoding.EncodeToString([]byte(spec.command))
	// Parse the exact original script separately, so constructs that must be
	// first (such as using namespace) do not follow the output setup statements.
	script := setup + "& ([ScriptBlock]::Create([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + command + "'))))"
	encodedRunes := utf16.Encode([]rune(script))
	encodedBytes := make([]byte, len(encodedRunes)*2)
	for i, v := range encodedRunes {
		encodedBytes[i*2], encodedBytes[i*2+1] = byte(v), byte(v>>8)
	}
	encoded := base64.StdEncoding.EncodeToString(encodedBytes)
	app, err := windows.UTF16PtrFromString(shell)
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	line, err := windows.UTF16PtrFromString(syscall.EscapeArg(shell) + " -NoLogo -NoProfile -NonInteractive -EncodedCommand " + encoded)
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	currentDir, err := windows.UTF16PtrFromString(home)
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	env := commandEnvironment()
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	si := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	si.ShowWindow, si.StdInput, si.StdOutput, si.StdErr = windows.SW_HIDE, stdin, outWrite, errWrite
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
	p := &managedProcess{job: job, stdout: out, stderr: errOut, out: make(chan capturedOutput, 1), errOut: make(chan capturedOutput, 1)}
	failure := gate(p, func() error {
		err := windows.CreateProcess(app, line, nil, nil, true, flags, &block[0], currentDir, &si.StartupInfo, &pi)
		if err == nil {
			p.process = pi.Process
			windows.CloseHandle(pi.Thread)
		}
		return err
	})
	if failure != "" {
		return nil, failure
	}
	keepJob, keepOut = true, true
	go func() { p.out <- capture(out, &p.stdoutData) }()
	go func() { p.errOut <- capture(errOut, &p.stderrData) }()
	return p, ""
}

func (p *managedProcess) wait(stop <-chan time.Duration, source clock.Source) processResult {
	defer windows.CloseHandle(p.job)
	defer windows.CloseHandle(p.process)
	var result processResult
	var outDone, errDone, outOK, errOK, exited bool
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var stopDeadline time.Duration
	stopping := false
	last := source().Elapsed
	for {
		n := source()
		if n.Wall.IsZero() || n.Elapsed < last || stopping && n.Elapsed >= stopDeadline {
			if !stopping {
				_ = windows.TerminateJobObject(p.job, 1)
			}
			p.stdout.Close()
			p.stderr.Close()
			return result
		}
		last = n.Elapsed
		if !exited {
			status, err := windows.WaitForSingleObject(p.process, 0)
			if err == nil && status == windows.WAIT_OBJECT_0 {
				exited = true
				var code uint32
				if windows.GetExitCodeProcess(p.process, &code) == nil {
					value := int(code)
					result.exitCode = &value
				}
			}
		}
		var info struct {
			UserTime, KernelTime, PeriodUserTime, PeriodKernelTime int64
			Faults, Total, Active, Terminated                      uint32
		}
		query := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
		if exited && outDone && errDone && outOK && errOK && query == nil && info.Active == 0 {
			result.confirmed = true
			return result
		}
		select {
		case output := <-p.out:
			result.stdout, outDone, outOK = output.output, true, output.ok
		case output := <-p.errOut:
			result.stderr, errDone, errOK = output.output, true, output.ok
		case deadline := <-stop:
			if !stopping {
				_ = windows.TerminateJobObject(p.job, 1)
				stopDeadline = deadline
			} else {
				stopDeadline = min(stopDeadline, deadline)
			}
			stopping = true
		case <-ticker.C:
		}
	}
}
