//go:build linux

package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type httpsProcess struct {
	done chan struct{}
	err  error
}

func startHTTPSProcess(t *testing.T, cmd *exec.Cmd) *httpsProcess {
	t.Helper()
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	}
	require(t, cmd.Start())
	p := &httpsProcess{done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
			return
		default:
		}
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
				t.Error("HTTPS test process did not exit after forced cleanup")
			}
			t.Error("HTTPS test process required forced cleanup")
		}
	})
	return p
}

func httpsCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require(t, err)
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Remota test CA"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	require(t, err)
	parent, err := x509.ParseCertificate(rootDER)
	require(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require(t, err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: root.NotBefore, NotAfter: root.NotAfter,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, parent, &leafKey.PublicKey, rootKey)
	require(t, err)
	return tls.Certificate{Certificate: [][]byte{leafDER, rootDER}, PrivateKey: leafKey}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})
}

// The browser owns a disposable NSS database. No system or real user roots are changed.
func startHTTPSBrowser(t *testing.T, ctx context.Context, executable string, ca []byte) string {
	t.Helper()
	base, err := filepath.Abs(os.TempDir())
	require(t, err)
	home, err := os.MkdirTemp(base, "remota-https-browser-")
	require(t, err)
	t.Cleanup(func() {
		if filepath.Dir(home) != base {
			t.Fatal("unexpected browser cleanup path")
		}
		require(t, os.RemoveAll(home))
	})
	nss := filepath.Join(home, ".pki", "nssdb")
	require(t, os.MkdirAll(nss, 0700))
	var credential *syscall.Credential
	if os.Geteuid() == 0 {
		credential = &syscall.Credential{Uid: 65534, Gid: 65534}
		for _, dir := range []string{home, filepath.Join(home, ".pki"), nss} {
			require(t, os.Chown(dir, 65534, 65534))
		}
	}
	cert := filepath.Join(home, "ca.pem")
	require(t, os.WriteFile(cert, ca, 0644))
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"), "TMPDIR=" + home}
	for _, args := range [][]string{{"-N", "-d", "sql:" + nss, "--empty-password"},
		{"-A", "-d", "sql:" + nss, "-n", "Remota test CA", "-t", "C,,", "-i", cert}} {
		cmd := exec.CommandContext(ctx, "certutil", args...)
		cmd.Env, cmd.SysProcAttr = env, &syscall.SysProcAttr{Credential: credential}
		require(t, cmd.Run())
	}
	profile := filepath.Join(home, "profile")
	cmd := exec.CommandContext(ctx, executable, "--headless=new", "--disable-setuid-sandbox", "--no-first-run", "--no-default-browser-check",
		"--disable-background-networking", "--remote-debugging-port=0", "--user-data-dir="+profile, "about:blank")
	cmd.Env, cmd.SysProcAttr = env, &syscall.SysProcAttr{Credential: credential, Setsid: true, Pdeathsig: syscall.SIGKILL}
	var output httpsText
	cmd.Stderr = &output
	p := startHTTPSProcess(t, cmd)
	var port string
	eventually(t, 15*time.Second, func() bool {
		select {
		case <-p.done:
			t.Fatalf("browser startup failed: %s", output.String())
		default:
		}
		b, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if err != nil {
			return false
		}
		port = strings.Split(string(b), "\n")[0]
		return port != ""
	})
	return "http://127.0.0.1:" + port
}

type httpsText struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *httpsText) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *httpsText) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type httpsAgent struct {
	process *httpsProcess
	master  *os.File
	text    httpsText
}

func startHTTPSAgent(t *testing.T, ctx context.Context, executable, origin, token, certificate string) *httpsAgent {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	require(t, err)
	master := os.NewFile(uintptr(fd), "https-agent-pty")
	require(t, unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0))
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	require(t, err)
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR, 0)
	require(t, err)
	defer slave.Close()
	a := &httpsAgent{master: master}
	readDone := make(chan struct{})
	go func() { io.Copy(&a.text, master); close(readDone) }()
	t.Cleanup(func() {
		master.Close()
		<-readDone
		if strings.Contains(a.text.String(), token) {
			t.Error("access credential was echoed by the product Agent")
		}
	})
	cmd := exec.CommandContext(ctx, executable, "--server", origin)
	emptyRoots := filepath.Join(filepath.Dir(certificate), "empty-roots")
	require(t, os.MkdirAll(emptyRoots, 0700))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "SSL_CERT_FILE=" + certificate, "SSL_CERT_DIR=" + emptyRoots}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0, Pdeathsig: syscall.SIGKILL}
	a.process = startHTTPSProcess(t, cmd)
	a.prompt(t, "Access credential:")
	eventually(t, 5*time.Second, func() bool {
		settings, err := unix.IoctlGetTermios(fd, unix.TCGETS)
		return err == nil && settings.Lflag&unix.ECHO == 0
	})
	a.send(t, token)
	return a
}

func (a *httpsAgent) send(t *testing.T, text string) {
	t.Helper()
	_, err := fmt.Fprintln(a.master, text)
	require(t, err)
}
func (a *httpsAgent) prompt(t *testing.T, prompt string) {
	t.Helper()
	eventually(t, 10*time.Second, func() bool {
		select {
		case <-a.process.done:
			t.Fatal("product Agent exited before the expected prompt")
		default:
		}
		return strings.Contains(a.text.String(), prompt)
	})
}
