package main

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"remota.local/remota/internal/server"
	"remota.local/remota/internal/server/store"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type ids []string

func (v *ids) String() string     { return strings.Join(*v, ",") }
func (v *ids) Set(s string) error { *v = append(*v, s); return nil }

func run(args []string, out, errout io.Writer) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(errout)
	var config server.Config
	var revoke ids
	var reset, inspect bool
	f.StringVar(&config.StateDir, "state-dir", "", "management state directory")
	switch command {
	case "serve":
		f.StringVar(&config.Listen, "listen", "127.0.0.1:8080", "loopback HTTP listen address")
		f.StringVar(&config.PublicURL, "public-url", "", "public HTTP(S) root URL")
		f.BoolVar(&config.AllowInsecureHTTP, "allow-insecure-http", false, "explicitly allow a non-loopback HTTP public URL")
		f.StringVar(&config.OperatorName, "operator-name", "Remota Support", "operator display name")
	case "recover":
		f.Var(&revoke, "revoke", "credential ID to revoke; may be repeated")
		f.BoolVar(&reset, "reset-admin", false, "replace the administrator password")
		f.BoolVar(&inspect, "inspect", false, "inspect durable metadata without changing it")
	case "init":
	default:
		return errors.New("expected init, serve or recover")
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if config.StateDir == "" || f.NArg() != 0 {
		return errors.New("a state directory is required; positional arguments are not accepted")
	}
	switch command {
	case "init":
		_, handoff, err := store.Initialize(config.StateDir, time.Now().UTC())
		if err != nil {
			return err
		}
		if err = json.MarshalWrite(out, handoff); err != nil {
			return errors.New("initialization committed; secret handoff failed; use local recovery")
		}
		if _, err = fmt.Fprintln(out); err != nil {
			return errors.New("initialization committed; secret handoff failed; use local recovery")
		}
		return nil
	case "recover":
		if inspect {
			if reset || len(revoke) > 0 {
				return errors.New("inspect cannot be combined with changes")
			}
			st, data, err := store.OpenRecovery(config.StateDir)
			if err != nil {
				return err
			}
			defer st.Close()
			rows := []map[string]any{}
			for _, r := range data.Credentials {
				v := r.Credential
				rows = append(rows, map[string]any{"id": v.ID, "kind": v.Kind, "name": v.Name, "creation_operation_id": v.CreationOperationID, "revoked": v.Revoked, "expires_at": v.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z")})
			}
			guard, pending, guardErr := st.Pending()
			var pendingInfo any
			if guardErr != nil {
				pendingInfo = map[string]any{"status": "invalid"}
			} else if pending {
				pendingInfo = map[string]any{"action": guard.Action, "operation_id": guard.OperationID, "target_id": guard.TargetID, "target_ids": guard.TargetIDs, "reset_admin": guard.ResetAdmin}
			}
			return json.MarshalWrite(out, map[string]any{"management_revision": data.Revision, "credentials": rows, "pending": pendingInfo})
		}
		data, password, err := store.Recover(config.StateDir, time.Now().UTC(), revoke, reset)
		if err != nil {
			return err
		}
		value := map[string]any{"management_revision": data.Revision}
		if password != "" {
			value["admin_password"] = password
		}
		if err = json.MarshalWrite(out, value); err != nil {
			return errors.New("recovery committed; handoff failed; repeat password recovery if needed")
		}
		return nil
	default:
		listener, err := net.Listen("tcp", config.Listen)
		if err != nil {
			return errors.New("cannot bind HTTP listener")
		}
		defer listener.Close()
		config.Listen = listener.Addr().String()
		service, err := server.Open(config)
		if err != nil {
			return err
		}
		defer service.Close()
		httpServer := &http.Server{Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10, ErrorLog: log.New(io.Discard, "", 0)}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		done := make(chan error, 1)
		go func() { done <- httpServer.Serve(listener) }()
		select {
		case err = <-done:
			if !errors.Is(err, http.ErrServerClosed) {
				return errors.New("HTTP service stopped unexpectedly")
			}
			return nil
		case <-ctx.Done():
			service.Core.Halt("shutting_down")
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if httpServer.Shutdown(shutdown) != nil {
				httpServer.Close()
			}
			return nil
		}
	}
}
