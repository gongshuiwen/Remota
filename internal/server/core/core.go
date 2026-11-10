package core

import (
	"sync"
	"time"

	"remota.local/remota/internal/auth"
	"remota.local/remota/internal/clock"
)

const (
	decisionWait   = 120 * time.Second
	activationWait = 15 * time.Second
	leaseWindow    = 30 * time.Second
	startWindow    = 5 * time.Second
	offerWait      = 60 * time.Second
	stopWait       = 5 * time.Second
	retention      = 300 * time.Second
	initWait       = 30 * time.Second
	idleWait       = 30 * time.Minute
)

type deadline struct {
	wall    time.Time
	elapsed time.Duration
}

func after(n clock.Sample, d time.Duration) deadline {
	return deadline{n.Wall.Add(d), n.Elapsed + d}
}

func earlier(a, b deadline) deadline {
	if b.wall.Before(a.wall) {
		a.wall = b.wall
	}
	if b.elapsed < a.elapsed {
		a.elapsed = b.elapsed
	}
	return a
}

func (d deadline) due(n clock.Sample) bool {
	return !n.Wall.Before(d.wall) || n.Elapsed >= d.elapsed
}

func (d deadline) remaining(n clock.Sample) int64 {
	r := min(d.wall.Sub(n.Wall), d.elapsed-n.Elapsed).Milliseconds()
	return max(0, r)
}

type credentialState struct {
	Credential
	expires deadline
	expired bool
	blocked bool
}

type clientState struct {
	Client
	expires, retain deadline
	lastSeen        clock.Sample
}

type sessionState struct {
	Session
	accessID, initialMode                        string
	expires, decision, activation, lease, retain deadline
	lastSeen                                     *clock.Sample
	leaseSeq                                     int64
	tasks                                        map[string]*taskState
	requests                                     map[string]string
	poll                                         *Poll
	endEvent                                     *Event
	endAck                                       bool
}

type taskState struct {
	Task
	offer, execute, window, confirm deadline
	grant                           *Grant
	result                          *Result
	receipts                        map[string]Receipt
	offerEvent, cancelEvent         *Event
	offerAck                        bool
}

type Core struct {
	mu                 sync.Mutex
	clock              clock.Source
	last               clock.Sample
	status, epoch      string
	ttl                time.Duration
	credentials        map[string]*credentialState
	clients            map[string]*clientState
	sessions           map[string]*sessionState
	digests            map[[32]byte]string
	admins             map[[32]byte]*adminSession
	operations         map[string]string
	managementRevision int64
	pending            *CredentialChange
}

// New requires an elapsed clock supplied by the application. No wall-clock-only
// fallback is allowed. TTL zero selects the documented default.
func New(source clock.Source, ttl time.Duration) (*Core, error) {
	if source == nil {
		return nil, InvalidArgument
	}
	if ttl == 0 {
		ttl = time.Hour
	}
	if ttl < time.Minute || ttl > 8*time.Hour || ttl%time.Second != 0 {
		return nil, InvalidArgument
	}
	n := source()
	if n.Wall.IsZero() || n.Elapsed < 0 {
		return nil, InvalidArgument
	}
	n.Wall = n.Wall.UTC().Truncate(time.Millisecond)
	n.Elapsed = n.Elapsed.Truncate(time.Millisecond)
	return &Core{clock: source, last: n, status: "ready", epoch: newID(""), ttl: ttl,
		credentials: make(map[string]*credentialState), clients: make(map[string]*clientState),
		sessions: make(map[string]*sessionState), digests: make(map[[32]byte]string),
		admins: make(map[[32]byte]*adminSession), operations: make(map[string]string)}, nil
}

func newID(prefix string) string {
	return auth.ID(prefix)
}

func (c *Core) writable() error {
	switch c.status {
	case "ready":
		return nil
	case "shutting_down":
		return ServerStopping
	default:
		return StateUnavailable
	}
}

// tick is called under mu at every boundary. Timers only need to call Sweep;
// request handlers cannot bypass expiration by racing a timer wakeup.
func (c *Core) tick() clock.Sample {
	n := c.clock()
	if n.Wall.IsZero() || n.Elapsed < c.last.Elapsed {
		n = c.last
		c.halt("degraded", "server_degraded", n)
		clear(c.admins)
	}
	// Protocol timestamps and budgets use a single millisecond-resolution sample.
	n.Wall = n.Wall.UTC().Truncate(time.Millisecond)
	n.Elapsed = n.Elapsed.Truncate(time.Millisecond)
	c.last = n
	c.expire(n)
	for digest, a := range c.admins {
		if a.expires.due(n) {
			delete(c.admins, digest)
		}
	}
	for id, s := range c.sessions {
		if s.State == "ended" && s.retain.due(n) {
			delete(c.sessions, id)
		}
	}
	for id, cl := range c.clients {
		if cl.Lifecycle == "ended" && cl.retain.due(n) {
			delete(c.clients, id)
		}
	}
	return n
}

func (c *Core) Sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
}

func (c *Core) credential(id, kind string) (*credentialState, error) {
	v := c.credentials[id]
	if v == nil || v.Kind != kind || v.Revoked || v.expired || v.blocked {
		return nil, Unauthenticated
	}
	return v, nil
}

func (c *Core) invalidateCredential(v *credentialState, n clock.Sample) {
	if v.Kind == "access" {
		for _, s := range c.sessions {
			if s.accessID == v.ID {
				c.endSession(s, "access_invalid", "credential_invalid", n)
			}
		}
	} else {
		for _, cl := range c.clients {
			if cl.CredentialID == v.ID {
				c.endClient(cl, "credential_invalid", "credential_invalid", n)
			}
		}
	}
}

func (c *Core) Halt(status string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.tick()
	if status != "degraded" && status != "shutting_down" {
		return InvalidArgument
	}
	reason := "server_degraded"
	if status == "shutting_down" {
		reason = "server_shutdown"
	}
	c.halt(status, reason, n)
	return nil
}

func (c *Core) halt(status, reason string, n clock.Sample) {
	if c.status == "shutting_down" {
		return
	}
	c.status = status
	stopReason := "session_ended"
	if status == "degraded" {
		stopReason = "server_degraded"
	}
	for _, s := range c.sessions {
		c.endSession(s, reason, stopReason, n)
	}
	for _, cl := range c.clients {
		c.endClient(cl, "server_shutdown", stopReason, n)
	}
}

// expire selects the earliest applicable condition, applies it and rescans the
// new phase. Absolute deadlines order causes even when elapsed time fires first.
// This deliberately uses a scan, not a second queue of mutable deadline state.
func (c *Core) expire(n clock.Sample) {
	for {
		var chosen func()
		var best deadline
		var rank, order int
		var resource string
		consider := func(d deadline, r, o int, id string, f func()) {
			if !d.due(n) {
				return
			}
			less := chosen == nil || d.wall.Before(best.wall) || (d.wall.Equal(best.wall) &&
				(r < rank || (r == rank && (o < order || (o == order && id < resource)))))
			if less {
				best, rank, order, resource, chosen = d, r, o, id, f
			}
		}
		for id, v := range c.credentials {
			if v.Revoked || v.expired {
				continue
			}
			o := 0
			if v.Kind == "mcp" {
				o = 1
			}
			consider(v.expires, 1, o, id, func() { v.expired = true; c.invalidateCredential(v, n) })
		}
		for id, s := range c.sessions {
			if s.State != "ended" {
				consider(s.expires, 2, 0, id, func() { c.endSession(s, "session_expired", "session_ended", n) })
				if s.State == "pending" {
					consider(s.decision, 2, 1, id, func() { c.endSession(s, "decision_timeout", "session_ended", n) })
				} else {
					d := s.lease
					if s.leaseSeq == 0 {
						d = s.activation
					}
					consider(d, 4, 0, id, func() { c.endSession(s, "lease_expired", "session_ended", n) })
				}
			}
			for tid, t := range s.tasks {
				if t.Phase == "queued" {
					consider(t.offer, 5, 0, tid, func() { c.settle(t, "timed_out", n) })
				}
				if t.Phase == "start_granted" && t.Stop == nil {
					consider(t.window, 5, 1, tid, func() { c.stopTask(t, "start_unconfirmed", n) })
				}
				if t.grant != nil && t.Phase != "settled" && t.Stop == nil {
					consider(t.execute, 6, 0, tid, func() { c.stopTask(t, "execution_deadline", n) })
				}
				if t.Phase == "stopping" {
					consider(t.confirm, 7, 0, tid, func() { c.unknown(s, t, n) })
				}
			}
		}
		for id, cl := range c.clients {
			if cl.Lifecycle == "ended" {
				continue
			}
			reason := "idle_timeout"
			o := 1
			if cl.Lifecycle == "initializing" {
				reason, o = "init_timeout", 0
			}
			consider(cl.expires, 3, o, id, func() { c.endClient(cl, reason, "mcp_client_ended", n) })
		}
		if chosen == nil {
			return
		}
		chosen()
	}
}

func ptr[T any](v T) *T { return &v }
func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	return ptr(*p)
}
