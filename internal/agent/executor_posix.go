//go:build linux || darwin

package agent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"remota.local/remota/internal/clock"
)

type managedProcess struct {
	cmd                    *exec.Cmd
	stdout, stderr         *os.File
	stdoutData, stderrData limitedOutput
	out, errOut            chan capturedOutput
	exit                   chan *int
}

func (p *managedProcess) discard() { p.stdoutData.discard(); p.stderrData.discard() }

func supportedShells() []string {
	if _, err := os.Stat("/bin/sh"); err == nil {
		return []string{"sh"}
	}
	return []string{}
}

// The process group is set up by the OS before the shell can execute the script.
func startProcess(spec commandSpec, gate spawnGate) (*managedProcess, string) {
	if spec.shell != "auto" && spec.shell != "sh" {
		return nil, "UNSUPPORTED_SHELL"
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		return nil, "UNSUPPORTED_SHELL"
	}
	home, err := commandHome()
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	out, outWrite, err := os.Pipe()
	if err != nil {
		return nil, "SPAWN_FAILED"
	}
	errOut, errWrite, err := os.Pipe()
	if err != nil {
		out.Close()
		outWrite.Close()
		return nil, "SPAWN_FAILED"
	}
	cmd := exec.Command("/bin/sh", "-c", spec.command)
	cmd.Dir, cmd.Env = home, commandEnvironment()
	cmd.Stdout, cmd.Stderr = outWrite, errWrite
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p := &managedProcess{cmd: cmd, stdout: out, stderr: errOut, out: make(chan capturedOutput, 1), errOut: make(chan capturedOutput, 1), exit: make(chan *int, 1)}
	failure := gate(p, cmd.Start)
	outWrite.Close()
	errWrite.Close()
	if failure != "" {
		out.Close()
		errOut.Close()
		return nil, failure
	}
	go func() { p.out <- capture(out, &p.stdoutData) }()
	go func() { p.errOut <- capture(errOut, &p.stderrData) }()
	go func() {
		_ = cmd.Wait()
		var code *int
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() >= 0 {
			value := cmd.ProcessState.ExitCode()
			code = &value
		}
		p.exit <- code
	}()
	return p, ""
}

// wait observes the entire managed group and both pipes, not just the shell.
// Cancellation always takes the forceful path; inability to prove termination
// within the existing stop window is reported to the caller as unconfirmed.
func (p *managedProcess) wait(stop <-chan time.Duration, source clock.Source) processResult {
	var result processResult
	var exitDone bool
	var outDone, errDone, outOK, errOK bool
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var stopDeadline time.Duration
	stopping := false
	last := source().Elapsed
	for {
		n := source()
		if n.Wall.IsZero() || n.Elapsed < last || stopping && n.Elapsed >= stopDeadline {
			if !stopping {
				_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGKILL)
			}
			p.stdout.Close()
			p.stderr.Close()
			return result
		}
		last = n.Elapsed
		groupErr := unix.Kill(-p.cmd.Process.Pid, 0)
		if exitDone && outDone && errDone && outOK && errOK && errors.Is(groupErr, unix.ESRCH) {
			result.confirmed = true
			break
		}
		select {
		case code := <-p.exit:
			result.exitCode, exitDone = code, true
		case output := <-p.out:
			result.stdout, outDone, outOK = output.output, true, output.ok
		case output := <-p.errOut:
			result.stderr, errDone, errOK = output.output, true, output.ok
		case deadline := <-stop:
			if !stopping {
				_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGKILL)
				stopDeadline = deadline
			} else {
				stopDeadline = min(stopDeadline, deadline)
			}
			stopping = true
		case <-ticker.C:
		}
	}
	return result
}
