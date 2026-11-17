package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

func TestNonInteractiveInputCannotApprove(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	oldArgs, oldFlags, oldStdin := os.Args, flag.CommandLine, os.Stdin
	defer func() { os.Args, flag.CommandLine, os.Stdin = oldArgs, oldFlags, oldStdin }()
	os.Args = []string{"remota-agent", "--server", "http://127.0.0.1:8080"}
	flag.CommandLine = flag.NewFlagSet("remota-agent", flag.ContinueOnError)
	os.Stdin = reader
	if err = run(); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("noninteractive input accepted: %v", err)
	}
}
