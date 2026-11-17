package server

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"remota.local/remota/internal/auth"
	"remota.local/remota/internal/clock"
	"remota.local/remota/internal/protocol"
	"remota.local/remota/internal/server/core"
	"remota.local/remota/internal/server/store"
)

const Version = "0.1.0-dev"

type Config struct {
	StateDir, Listen, PublicURL, OperatorName string
	AllowInsecureHTTP                         bool
	Clock                                     clock.Source // application/test construction only, never a remote option
}

type Server struct {
	Core                          *core.Core
	config                        Config
	validator                     *protocol.Validator
	mcp                           *mcpGateway
	store                         *store.Store
	password                      auth.PasswordHash
	authority, origin, publicMode string
	secure                        bool
	writeMu                       sync.Mutex
	loginMu                       sync.Mutex
	failures                      []loginFailure
	persist                       func(context.Context, store.Change) store.Outcome
	cancel                        context.CancelFunc
	wg                            sync.WaitGroup
	closeOnce                     sync.Once
	closeErr                      error
}
type loginFailure struct {
	source string
	at     clock.Sample
}

func normalizeAuthority(s, scheme string) (string, error) {
	u, err := url.Parse("//" + s)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(s, "#") {
		return "", core.InvalidArgument
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if host == "" || strings.ContainsAny(host, " \t\r\n") {
		return "", core.InvalidArgument
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || host == "" {
			return "", core.InvalidArgument
		}
		host = strings.ToLower(host)
	}
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", core.InvalidArgument
	}
	return net.JoinHostPort(host, strconv.FormatUint(portNumber, 10)), nil
}

func (c *Config) validate() (authority, origin, mode string, err error) {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.OperatorName == "" {
		c.OperatorName = "Remota Support"
	}
	host, _, e := net.SplitHostPort(c.Listen)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || !utf8.ValidString(c.OperatorName) || len(c.OperatorName) > 80 {
		return "", "", "", core.InvalidArgument
	}
	address := "http://" + c.Listen
	mode = "loopback_http"
	if c.PublicURL != "" {
		address = c.PublicURL
	}
	u, e := url.Parse(address)
	if e != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || strings.Contains(address, "#") {
		return "", "", "", core.InvalidArgument
	}
	if u.Scheme == "https" {
		mode = "https"
	} else if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
		if !c.AllowInsecureHTTP {
			return "", "", "", core.InvalidArgument
		}
		mode = "insecure_http"
	}
	authority, e = normalizeAuthority(u.Host, u.Scheme)
	if e != nil {
		return "", "", "", e
	}
	origin = u.Scheme + "://" + authority
	return authority, origin, mode, nil
}

func Open(config Config) (*Server, error) {
	a, o, m, err := config.validate()
	if err != nil {
		return nil, err
	}
	if config.Clock == nil {
		config.Clock, err = clock.NewReal()
		if err != nil {
			return nil, err
		}
	}
	st, data, err := store.Open(config.StateDir)
	if err != nil {
		return nil, err
	}
	c, err := core.New(config.Clock, 0)
	if err != nil {
		st.Close()
		return nil, err
	}
	if err = c.InstallManagement(data.Revision, data.Credentials); err != nil {
		st.Close()
		return nil, err
	}
	v, err := protocol.NewValidator()
	if err != nil {
		st.Close()
		return nil, err
	}
	s := &Server{Core: c, config: config, validator: v, store: st, password: data.Admin, authority: a, origin: o, publicMode: m, secure: m == "https", persist: st.Apply}
	s.mcp, err = newMCPGateway(s)
	if err != nil {
		st.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		ticks := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.Sweep()
				ticks++
				if ticks%20 == 0 {
					s.mcp.prune()
				}
			}
		}
	}()
	return s, nil
}

func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.Core.Halt("shutting_down")
		s.cancel()
		s.wg.Wait()
		s.mcp.close()
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		s.closeErr = s.store.Close()
	})
	return s.closeErr
}

func (s *Server) mutateCredential(token, csrf, action, target string, input core.CredentialInput) (core.Credential, string, core.Effects, int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	change, err := s.Core.PrepareCredential(token, csrf, action, target, input)
	if err != nil {
		return core.Credential{}, "", core.Effects{}, 0, err
	}
	if change.Noop {
		v, rev, err := s.Core.CompleteCredential(change, [32]byte{})
		return v, "", change.Effects, rev, err
	}
	secret := ""
	var record *core.CredentialRecord
	var digest [32]byte
	if change.Credential != nil {
		secret = auth.Secret("rm_" + change.Credential.Kind + "_")
		digest = auth.Digest(secret)
		record = &core.CredentialRecord{Credential: *change.Credential, Digest: digest}
	}
	write := store.Change{ExpectedRevision: change.Revision - 1, Credential: record}
	if change.Previous != nil {
		write.RevokeIDs = []string{change.Previous.ID}
		write.RevokedAt = *change.Previous.RevokedAt
	}
	guard := store.Guard{Action: action, TargetID: target, OperationID: input.OperationID}
	if err = s.store.WriteGuard(guard); err != nil {
		s.Core.FailCredential(change)
		return core.Credential{}, "", core.Effects{}, 0, core.StateUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if s.persist(ctx, write) != store.Committed {
		s.Core.FailCredential(change)
		return core.Credential{}, "", core.Effects{}, 0, core.StateUnavailable
	}
	if err = s.store.ClearGuard(); err != nil {
		s.Core.FailCredential(change)
		return core.Credential{}, "", core.Effects{}, 0, core.StateUnavailable
	}
	v, rev, err := s.Core.CompleteCredential(change, digest)
	if err != nil {
		return v, "", change.Effects, rev, err
	}
	return v, secret, change.Effects, rev, nil
}
