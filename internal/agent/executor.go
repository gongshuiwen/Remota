package agent

import (
	"bytes"
	"io"
	"os"
	"os/user"
	"runtime"
	"sync"
)

const outputLimit = 8192

type commandSpec struct {
	command, reason, shell string
	timeoutSeconds         int64
}

type commandOutput struct {
	text          string
	capturedBytes int
	truncated     bool
}

func (o commandOutput) value() map[string]any {
	return map[string]any{"text": o.text, "captured_bytes": o.capturedBytes, "truncated": o.truncated}
}

type limitedOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
	disabled  bool
}

func (o *limitedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(p)
	if o.disabled {
		return n, nil
	}
	remaining := outputLimit - len(o.data)
	if n > remaining {
		o.truncated = true
		p = p[:remaining]
	}
	o.data = append(o.data, p...)
	return n, nil
}

func (o *limitedOutput) value() commandOutput {
	o.mu.Lock()
	defer o.mu.Unlock()
	return commandOutput{text: string(bytes.ToValidUTF8(o.data, []byte("\uFFFD"))), capturedBytes: len(o.data), truncated: o.truncated}
}

func (o *limitedOutput) discard() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.disabled, o.data, o.truncated = true, nil, false
}

type capturedOutput struct {
	output commandOutput
	ok     bool
}

func capture(r *os.File, output *limitedOutput) capturedOutput {
	defer r.Close()
	_, err := io.Copy(output, r)
	return capturedOutput{output: output.value(), ok: err == nil}
}

type processResult struct {
	exitCode       *int
	stdout, stderr commandOutput
	confirmed      bool
}

// Preparations happen outside the authorization lock; the gate rechecks it
// immediately around the OS process-creation call.
type spawnGate func(*managedProcess, func() error) string

// Only documented variables reach the child, and the shell path is selected
// separately by each platform instead of being resolved through PATH.
func commandEnvironment() []string {
	names := []string{"HOME", "USER", "LOGNAME", "PATH", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "TZ"}
	if runtime.GOOS == "windows" {
		names = []string{"SystemRoot", "WINDIR", "ComSpec", "TEMP", "TMP", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "PATH", "PATHEXT"}
	}
	env := make([]string, 0, len(names))
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok {
			if name == "LC_ALL" && runtime.GOOS != "windows" {
				continue
			}
			env = append(env, name+"="+value)
		}
	}
	if runtime.GOOS == "linux" {
		env = append(env, "LC_ALL=C.UTF-8")
	} else if runtime.GOOS == "darwin" {
		env = append(env, "LC_ALL=en_US.UTF-8")
	}
	return env
}

func commandHome() (string, error) {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		if err == nil {
			err = ErrProtocol
		}
		return "", err
	}
	return u.HomeDir, nil
}
