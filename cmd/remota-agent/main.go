package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"remota.local/remota/internal/agent"
	"remota.local/remota/internal/auth"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	address := flag.String("server", "", "server root URL")
	insecure := flag.Bool("allow-insecure-http", false, "allow non-loopback HTTP")
	flag.Parse()
	if *address == "" || flag.NArg() != 0 {
		return errors.New("a server root URL is required")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("an interactive terminal is required")
	}
	a, err := agent.New(*address, *insecure)
	if err != nil {
		return errors.New("invalid server address or unavailable clock")
	}
	fmt.Fprint(os.Stderr, "Access credential: ")
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil || !auth.ValidSecret(string(secret), "access") {
		return errors.New("invalid access credential")
	}
	access := string(secret)
	clear(secret)
	input := make(chan string)
	go func() {
		defer close(input)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			input <- scanner.Text()
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.Run(ctx, access, input, os.Stdout)
}
