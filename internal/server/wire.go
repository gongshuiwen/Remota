package server

import (
	"remota.local/remota/internal/server/core"
	"time"
)

type object = map[string]any

func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func effects(e core.Effects) object {
	return object{"sessions_ended": e.SessionsEnded, "clients_ended": e.ClientsEnded, "tasks_stop_requested": e.TasksStopRequested}
}

func credential(v core.Credential) object {
	r := object{"id": v.ID, "kind": v.Kind, "name": v.Name, "created_at": timestamp(v.CreatedAt), "expires_at": timestamp(v.ExpiresAt), "status": v.Status,
		"creation_operation_id": v.CreationOperationID, "effects_count": object{"live_sessions": v.EffectsCount.LiveSessions, "live_clients": v.EffectsCount.LiveClients, "unsettled_tasks": v.EffectsCount.UnsettledTasks}}
	if v.Kind == "mcp" {
		r["allow_exec"] = v.AllowExec
	}
	if v.RevokedAt != nil {
		r["revoked_at"] = timestamp(*v.RevokedAt)
	}
	if v.RotatedFromID != "" {
		r["rotated_from_id"] = v.RotatedFromID
	}
	return r
}

func (s *Server) session(v core.Session, admin bool) object {
	connection := object{"activity": v.Activity}
	if v.LastSeenAt != nil {
		connection["last_seen_at"] = timestamp(*v.LastSeenAt)
	}
	r := object{"id": v.ID, "state": v.State, "operator_display_name": s.config.OperatorName, "purpose": "temporary_remote_assistance",
		"created_at": timestamp(v.CreatedAt), "expires_at": timestamp(v.ExpiresAt), "decision_deadline": timestamp(v.DecisionDeadline), "connection": connection, "granted_capabilities": v.Capabilities}
	if v.Consent != nil {
		r["consent"] = object{"mode": v.Consent.Mode, "revision": v.Consent.Revision, "decided_at": timestamp(v.Consent.DecidedAt)}
	}
	if v.EndedAt != nil {
		r["ended_at"] = timestamp(*v.EndedAt)
		r["end_reason"] = v.EndReason
		r["cleanup"] = v.Cleanup
	}
	if admin {
		r["access_credential_id"] = v.AccessCredentialID
		r["note"] = v.Note
		r["note_revision"] = v.NoteRevision
	}
	return r
}

func client(v core.Client) object {
	r := object{"id": v.ID, "credential_id": v.CredentialID, "client_info": object{"name": v.Info.Name, "version": v.Info.Version}, "lifecycle": v.Lifecycle, "activity": v.Activity,
		"created_at": timestamp(v.CreatedAt), "last_seen_at": timestamp(v.LastSeenAt), "expires_at": timestamp(v.ExpiresAt)}
	if v.EndedAt != nil {
		r["ended_at"] = timestamp(*v.EndedAt)
		r["end_reason"] = v.EndReason
	}
	return r
}

func spec(v core.Spec) object {
	return object{"command": v.Command, "reason": v.Reason, "shell": v.Shell, "timeout_seconds": v.TimeoutSeconds}
}

func output(v core.Output) object {
	return object{"text": v.Text, "captured_bytes": v.CapturedBytes, "truncated": v.Truncated}
}

func task(v core.Task) object {
	execution := object{"state": v.Execution.State}
	if v.Execution.StartObservedAt != nil {
		execution["start_observed_at"] = timestamp(*v.Execution.StartObservedAt)
	}
	if v.Execution.ExitObservedAt != nil {
		execution["exit_observed_at"] = timestamp(*v.Execution.ExitObservedAt)
	}
	if v.Execution.ExitCode != nil {
		execution["exit_code"] = *v.Execution.ExitCode
	}
	r := object{"id": v.ID, "session_id": v.SessionID, "request_id": v.RequestID, "phase": v.Phase,
		"consent_revision": v.ConsentRevision, "created_at": timestamp(v.CreatedAt), "offer_deadline": timestamp(v.OfferDeadline),
		"initiator": object{"credential_id": v.CredentialID, "mcp_client_id": v.ClientID}, "execution": execution, "details_available": v.DetailsAvailable}
	if v.ExecutionDeadline != nil {
		r["execution_deadline"] = timestamp(*v.ExecutionDeadline)
	}
	if v.SettledAt != nil {
		r["settled_at"] = timestamp(*v.SettledAt)
	}
	if v.Outcome != "" {
		r["outcome"] = v.Outcome
	}
	if v.Stop != nil {
		r["stop"] = object{"reason": v.Stop.Reason, "requested_at": timestamp(v.Stop.RequestedAt), "confirmation_deadline": timestamp(v.Stop.ConfirmationDeadline)}
	}
	if v.ErrorCode != "" {
		r["error_code"] = v.ErrorCode
	}
	if v.Spec != nil {
		r["spec"] = spec(*v.Spec)
	}
	if v.Result != nil {
		r["result"] = object{"stdout": output(v.Result.Stdout), "stderr": output(v.Result.Stderr)}
	}
	return r
}

func grant(v core.Grant) object {
	return object{"id": v.ID, "session_id": v.SessionID, "task_id": v.TaskID, "consent_revision": v.ConsentRevision,
		"spec": spec(v.Spec), "issued_at": timestamp(v.IssuedAt), "execute_before": timestamp(v.ExecuteBefore),
		"execution_deadline": timestamp(v.ExecutionDeadline), "start_window_ms": v.StartWindowMS, "execution_budget_ms": v.ExecutionBudgetMS}
}

func (s *Server) snapshot(v core.Snapshot, a core.AdminLogin) object {
	server := object{"version": Version, "server_epoch": v.Epoch, "health": v.Status, "management_revision": v.ManagementRevision}
	if v.Status == "shutting_down" {
		server["health"] = "degraded"
	}
	if s.config.PublicURL != "" {
		server["public_url"] = s.config.PublicURL
	}
	summary := object{"live_sessions": 0, "cleanup_pending_sessions": 0, "ready_clients": 0, "active_access_credentials": 0, "active_mcp_credentials": 0, "expiring_credentials": 0, "expiring_sessions": 0}
	inc := func(key string) { summary[key] = summary[key].(int) + 1 }
	sessions, clients, credentials := []object{}, []object{}, []object{}
	for _, x := range v.Sessions {
		sessions = append(sessions, s.session(x, true))
		if x.State != "ended" {
			inc("live_sessions")
			if x.RemainingMS > 0 && x.RemainingMS <= 300000 {
				inc("expiring_sessions")
			}
		} else if x.Cleanup == "pending" {
			inc("cleanup_pending_sessions")
		}
	}
	for _, x := range v.Clients {
		clients = append(clients, client(x))
		if x.Lifecycle == "ready" {
			inc("ready_clients")
		}
	}
	for _, x := range v.Credentials {
		credentials = append(credentials, credential(x))
		if x.Status == "active" {
			inc("active_" + x.Kind + "_credentials")
			if x.RemainingMS > 0 && x.RemainingMS <= (7*24*time.Hour).Milliseconds() {
				inc("expiring_credentials")
			}
		}
	}
	return object{"server": server, "server_time": timestamp(v.Time), "csrf_token": a.CSRF, "admin_session": object{"expires_at": timestamp(a.ExpiresAt)},
		"transport": object{"public_mode": s.publicMode, "backend_mode": "http"}, "summary": summary, "sessions": sessions, "clients": clients, "credentials": credentials}
}
