package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"remota.local/remota/internal/server/store"
)

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("handoff unavailable") }

func TestInitializationHandoffCannotBeReplayed(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	if err := run([]string{"init", "--state-dir", dir}, brokenWriter{}, &stderr); err == nil {
		t.Fatal("handoff failure hidden")
	}
	s, data, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if len(data.Credentials) != 2 {
		t.Fatal("handoff failure rolled back initialized identity")
	}
	var out bytes.Buffer
	if err = run([]string{"init", "--state-dir", dir}, &out, &stderr); err == nil || out.Len() != 0 {
		t.Fatal("initial secrets replayed")
	}
	if err = run([]string{"recover", "--inspect", "--state-dir", dir}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "digest") || strings.Contains(out.String(), "salt") || strings.Contains(out.String(), "admin_password") {
		t.Fatal("inspection leaked authentication material")
	}
	out.Reset()
	if err = run([]string{"recover", "--reset-admin", "--state-dir", dir}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "admin_password") {
		t.Fatal("local password recovery missing")
	}
}
