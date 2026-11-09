// Package core owns the in-memory control state. Callers authenticate secrets and
// validate wire schemas before entering it; core checks resource binding, current
// authority, deadlines and transitions. It performs no I/O or process execution.
package core

import "time"

// Code is a contract error without input, secrets or underlying system errors.
type Code string

func (e Code) Error() string { return string(e) }

const (
	InvalidArgument     Code = "INVALID_ARGUMENT"
	Unauthenticated     Code = "UNAUTHENTICATED"
	NotFound            Code = "NOT_FOUND"
	TaskNotFound        Code = "TASK_NOT_FOUND"
	ClientNotFound      Code = "MCP_SESSION_NOT_FOUND"
	SessionEnded        Code = "SESSION_ENDED"
	InvalidState        Code = "INVALID_STATE"
	ConsentRequired     Code = "LOCAL_CONSENT_REQUIRED"
	Reconsent           Code = "LOCAL_RECONSENT_REQUIRED"
	ExecNotAllowed      Code = "EXEC_NOT_ALLOWED"
	RevisionConflict    Code = "REVISION_CONFLICT"
	IdempotencyConflict Code = "IDEMPOTENCY_CONFLICT"
	ResultConflict      Code = "RESULT_CONFLICT"
	GrantExpired        Code = "GRANT_EXPIRED"
	TaskBusy            Code = "TASK_BUSY"
	TaskLimit           Code = "TASK_LIMIT"
	CapacityExceeded    Code = "CAPACITY_EXCEEDED"
	AlreadyPolling      Code = "ALREADY_POLLING"
	AgentUnavailable    Code = "AGENT_UNAVAILABLE"
	StateUnavailable    Code = "STATE_UNAVAILABLE"
	ServerStopping      Code = "SERVER_STOPPING"
)

// These are domain values, not wire DTOs. Transport adapters own JSON field
// names, fixed-millisecond timestamps, authentication tokens and response shapes.
type Credential struct {
	ID, Kind  string // access or mcp
	AllowExec bool
	ExpiresAt time.Time
	Revoked   bool
}

type Client struct {
	ID, CredentialID, Lifecycle, Activity string
	CreatedAt, LastSeenAt, ExpiresAt      time.Time
	EndedAt                               *time.Time
	EndReason                             string
}

type Consent struct {
	Mode      string
	Revision  int64
	DecidedAt time.Time
}

type Session struct {
	ID, State                              string
	CreatedAt, ExpiresAt, DecisionDeadline time.Time
	Consent                                *Consent
	Capabilities                           []string
	Activity                               string
	LastSeenAt                             *time.Time
	EndedAt                                *time.Time
	EndReason, Cleanup                     string
	// Budgets are internal projections for transport response construction.
	RemainingMS, DecisionRemainingMS, ActivationRemainingMS int64
}

type Lease struct {
	Seq, ConsentRevision int64
	IssuedAt, ExpiresAt  time.Time
	ValidForMS           int64
}

type Spec struct {
	Command, Reason, Shell string
	TimeoutSeconds         int
}

type Execution struct {
	State                           string
	StartObservedAt, ExitObservedAt *time.Time
	ExitCode                        *int
}

type Stop struct {
	Reason                            string
	RequestedAt, ConfirmationDeadline time.Time
}

type Output struct {
	Text          string
	CapturedBytes int
	Truncated     bool
}

type TaskOutput struct{ Stdout, Stderr Output }

type Task struct {
	ID, SessionID, RequestID, Phase, Outcome string
	CredentialID, ClientID                   string
	ConsentRevision                          int64
	CreatedAt, OfferDeadline                 time.Time
	ExecutionDeadline, SettledAt             *time.Time
	Execution                                Execution
	Stop                                     *Stop
	ErrorCode                                string
	DetailsAvailable                         bool
	Spec                                     *Spec
	Result                                   *TaskOutput
}

type Grant struct {
	ID, SessionID, TaskID                      string
	ConsentRevision                            int64
	Spec                                       Spec
	IssuedAt, ExecuteBefore, ExecutionDeadline time.Time
	StartWindowMS, ExecutionBudgetMS           int64
}

type Receipt struct {
	Kind, GrantID, Reason, ErrorCode string
	ExitCode                         *int
}

type Result struct {
	GrantID, Outcome, ErrorCode string
	ConsentRevision             int64
	ExitCode                    *int
	Stdout, Stderr              Output
}

// Event payloads are explicit instead of untyped maps. Each event is immutable.
type Event struct {
	ID, Type, SessionID, Reason string
	CreatedAt                   time.Time
	Deadline, EndedAt           *time.Time
	Task                        *Task
	TaskID                      string
	ConfirmationDeadline        *time.Time
}

type PollResult struct {
	Session            Session
	SessionRemainingMS int64
	Lease              *Lease
	Events             []Event
}

// Snapshot is an internal coherent projection, not the admin HTTP response.
// Tasks contain metadata only. An adapter may form narrower authenticated views.
type Snapshot struct {
	Status, Epoch string
	Sessions      []Session
	Clients       []Client
	Tasks         []Task
}
