package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"remota.local/remota/internal/clock"
)

func TestWindowsProcessOutputAndExit(t *testing.T) {
	t.Setenv("REMOTA_TEST_SECRET", "must-not-be-inherited")
	for _, test := range []struct {
		name, command  string
		code           int
		stdout, stderr commandOutput
	}{
		{"bounded_output", `if ($env:REMOTA_TEST_SECRET) { [Console]::Out.Write('leaked') } else { [Console]::Out.Write('unset') }; if ([Console]::In.Read() -eq -1) { [Console]::Out.Write('eof') }; [Console]::Out.Write(('x' * 9000)); [Console]::Error.Write('error'); exit 7`,
			7, commandOutput{"unseteof" + strings.Repeat("x", outputLimit-8), outputLimit, true}, commandOutput{"error", 5, false}},
		{"script_header", `using namespace System.Text
param([string]$value = 'hello')
	[Console]::Out.Write([StringBuilder]::new().Append($value).ToString())`,
			0, commandOutput{"hello", 5, false}, commandOutput{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, reason := startProcess(commandSpec{shell: "powershell", command: test.command}, allowTestSpawn)
			if p == nil {
				t.Fatalf("spawn failed: %s", reason)
			}
			source, err := clock.NewReal()
			if err != nil {
				t.Fatal(err)
			}
			stop := make(chan time.Duration, 1)
			timer := time.AfterFunc(10*time.Second, func() { stop <- source().Elapsed + time.Second })
			defer timer.Stop()
			r := p.wait(stop, source)
			if !r.confirmed || r.exitCode == nil || *r.exitCode != test.code || r.stdout != test.stdout || r.stderr != test.stderr {
				t.Fatalf("unexpected output or exit: %+v", r)
			}
		})
	}
}

func TestWindowsJobCancellation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child.pid")
	command := fmt.Sprintf(`$p = Start-Process -FilePath $env:ComSpec -ArgumentList '/c ping -n 30 127.0.0.1 >NUL' -PassThru; [System.IO.File]::WriteAllText('%s', $p.Id); Start-Sleep -Seconds 30`, strings.ReplaceAll(marker, "'", "''"))
	p, reason := startProcess(commandSpec{shell: "auto", command: command}, allowTestSpawn)
	if p == nil {
		t.Fatalf("spawn failed: %s", reason)
	}
	source, err := clock.NewReal()
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan time.Duration, 1)
	var child windows.Handle
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if data, e := os.ReadFile(marker); e == nil {
			pid, e := strconv.Atoi(string(data))
			if e != nil {
				t.Fatal(e)
			}
			child, e = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
			if e != nil {
				t.Fatal(e)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if child == 0 {
		stop <- source().Elapsed + 5*time.Second
		_ = p.wait(stop, source)
		t.Fatal("child process did not start")
	}
	defer windows.CloseHandle(child)
	stop <- source().Elapsed + 5*time.Second
	if r := p.wait(stop, source); !r.confirmed {
		t.Fatal("Job did not confirm cleanup")
	}
	if status, err := windows.WaitForSingleObject(child, 0); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("child still running: %d %v", status, err)
	}
}
