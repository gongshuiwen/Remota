package integration

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"remota.local/remota/internal/server"
	"remota.local/remota/internal/server/store"
)

// Browser tests use the real admin HTTP surface and a fresh Linux state directory.
func TestConsoleServer(t *testing.T) {
	if os.Getenv("REMOTA_CONSOLE_TEST") != "1" {
		t.Skip("private browser-test server")
	}
	dir := t.TempDir()
	_, handoff, err := store.Initialize(dir, time.Now().UTC())
	require(t, err)
	listener, err := net.Listen("tcp", os.Getenv("REMOTA_CONSOLE_LISTEN"))
	require(t, err)
	defer listener.Close()
	service, err := server.Open(server.Config{StateDir: dir, Listen: listener.Addr().String(), PublicURL: os.Getenv("REMOTA_CONSOLE_PUBLIC_URL")})
	require(t, err)
	defer service.Close()
	web := &http.Server{Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second}
	defer web.Close()
	go web.Serve(listener)
	require(t, json.NewEncoder(os.Stdout).Encode(map[string]string{"password": handoff.AdminPassword, "access": handoff.Access, "mcp": handoff.MCP}))
	_, err = io.Copy(io.Discard, os.Stdin)
	require(t, err)
}
