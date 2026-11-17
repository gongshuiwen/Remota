package integration

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type terminalText struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *terminalText) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *terminalText) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type agentTerminal struct {
	input   *os.File
	process windows.Handle
	text    terminalText
}

func startTerminal(t *testing.T, executable, address, access string) *agentTerminal {
	t.Helper()
	// ConPTY requires synchronous pipes; os.Pipe can return overlapped handles.
	pipe := func() (*os.File, *os.File) {
		var read, write windows.Handle
		require(t, windows.CreatePipe(&read, &write, nil, 0))
		return os.NewFile(uintptr(read), "conpty-read"), os.NewFile(uintptr(write), "conpty-write")
	}
	inputRead, inputWrite := pipe()
	defer inputRead.Close()
	t.Cleanup(func() { inputWrite.Close() })
	outputRead, outputWrite := pipe()
	defer outputWrite.Close()
	t.Cleanup(func() { outputRead.Close() })
	a := &agentTerminal{input: inputWrite}
	readDone := make(chan struct{})
	go func() { io.Copy(&a.text, outputRead); close(readDone) }()
	var console windows.Handle
	require(t, windows.CreatePseudoConsole(windows.Coord{X: 160, Y: 40}, windows.Handle(inputRead.Fd()), windows.Handle(outputWrite.Fd()), 0, &console))
	t.Cleanup(func() {
		if a.process != 0 {
			if !a.stop() {
				windows.TerminateProcess(a.process, 1)
				windows.WaitForSingleObject(a.process, 5000)
			}
			windows.CloseHandle(a.process)
		}
		windows.ClosePseudoConsole(console)
		outputRead.Close()
		<-readDone
		if strings.Contains(a.text.String(), access) {
			t.Error("access credential was echoed by the native Windows terminal")
		}
	})
	attributes, err := windows.NewProcThreadAttributeList(1)
	require(t, err)
	defer attributes.Delete()
	// This attribute takes HPCON by value, unlike handle-list attributes. Pass
	// its integer value directly instead of constructing an invalid Go pointer.
	update := windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")
	updated, _, err := update.Call(uintptr(unsafe.Pointer(attributes.List())), 0,
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(console), unsafe.Sizeof(console), 0, 0)
	if updated == 0 {
		t.Fatalf("pseudoconsole attribute failed: %v", err)
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	// Null standard handles select the new console instead of the test runner's
	// redirected standard streams.
	si.Cb, si.Flags, si.ShowWindow = uint32(unsafe.Sizeof(si)), windows.STARTF_USESHOWWINDOW|windows.STARTF_USESTDHANDLES, windows.SW_HIDE
	app, err := windows.UTF16PtrFromString(executable)
	require(t, err)
	line, err := windows.UTF16PtrFromString(syscall.EscapeArg(executable) + " --server " + syscall.EscapeArg(address))
	require(t, err)
	var process windows.ProcessInformation
	require(t, windows.CreateProcess(app, line, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT, nil, nil, &si.StartupInfo, &process))
	a.process = process.Process
	windows.CloseHandle(process.Thread)
	a.prompt(t, "Access credential:")
	a.send(t, access)
	a.prompt(t, "Press Enter to accept")
	return a
}

func (a *agentTerminal) send(t *testing.T, text string) {
	t.Helper()
	_, err := fmt.Fprint(a.input, text+"\r")
	require(t, err)
}

func (a *agentTerminal) prompt(t *testing.T, text string) {
	t.Helper()
	eventually(t, 15*time.Second, func() bool {
		if status, _ := windows.WaitForSingleObject(a.process, 0); status == windows.WAIT_OBJECT_0 {
			var code uint32
			windows.GetExitCodeProcess(a.process, &code)
			t.Fatalf("native Agent exited before terminal confirmation: code %d", code)
		}
		return strings.Contains(a.text.String(), text)
	})
}

func (a *agentTerminal) finish(t *testing.T) {
	t.Helper()
	if !a.stop() {
		t.Fatal("native Agent did not end")
	}
	var code uint32
	require(t, windows.GetExitCodeProcess(a.process, &code))
	if code != 0 {
		t.Fatalf("native Agent exit code %d", code)
	}
}

func (a *agentTerminal) stop() bool {
	status, err := windows.WaitForSingleObject(a.process, 0)
	if err == nil && status != windows.WAIT_OBJECT_0 {
		if _, err = fmt.Fprint(a.input, "q\r"); err != nil {
			return false
		}
		status, err = windows.WaitForSingleObject(a.process, 12000)
	}
	return err == nil && status == windows.WAIT_OBJECT_0
}
