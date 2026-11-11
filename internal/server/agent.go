package server

import (
	"net/http"
	"strings"
	"time"

	"remota.local/remota/internal/protocol"
	"remota.local/remota/internal/server/core"
)

func agentRoute(path string) (action, sid, id, method string) {
	p := strings.Split(strings.TrimPrefix(path, "/api/v1/agent/"), "/")
	if len(p) == 1 && p[0] == "sessions" {
		return "register", "", "", "POST"
	}
	if len(p) == 2 && p[0] == "sessions" && p[1] != "" {
		return "get", p[1], "", "GET"
	}
	if len(p) == 3 && p[0] == "sessions" && p[1] != "" {
		switch p[2] {
		case "decision", "consent":
			return p[2], p[1], "", "PUT"
		case "poll", "close":
			return p[2], p[1], "", "POST"
		}
	}
	if len(p) == 5 && p[0] == "sessions" && p[1] != "" && p[2] == "calls" && p[3] != "" && p[4] == "result" {
		return "call_result", p[1], p[3], "POST"
	}
	return "", "", "", ""
}

func agentBearer(r *http.Request) (string, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || len(r.Header.Values("Cookie")) != 0 {
		return "", core.Unauthenticated
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", core.Unauthenticated
	}
	return parts[1], nil
}

func (s *Server) failAgent(w http.ResponseWriter, err error) {
	if err == core.NotFound {
		err = core.Unauthenticated
	}
	s.fail(w, err)
}

func (s *Server) serveAgentHTTP(w http.ResponseWriter, r *http.Request) {
	action, sid, id, method := agentRoute(r.URL.Path)
	if action == "" || r.URL.EscapedPath() != r.URL.Path {
		s.fail(w, core.NotFound)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		s.fail(w, core.InvalidArgument)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		s.respond(w, 405, object{"error": object{"code": "INVALID_ARGUMENT", "message": "method not allowed", "trace_id": w.Header().Get("X-Request-ID")}})
		return
	}
	if method == "GET" && (r.ContentLength > 0 || len(r.TransferEncoding) > 0) {
		s.fail(w, core.InvalidArgument)
		return
	}
	if len(r.Header.Values("Origin")) != 0 {
		s.fail(w, core.Code("ORIGIN_DENIED"))
		return
	}
	token, err := agentBearer(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var access string
	if action == "register" {
		access, err = s.Core.AuthenticateCredential(token, "access")
	} else {
		err = s.Core.AuthenticateAgent(sid, token)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if action == "call_result" && !core.ValidID(id, "call") {
		s.fail(w, core.InvalidArgument)
		return
	}
	if action == "get" {
		v, err := s.Core.GetSession(sid)
		if err != nil {
			s.fail(w, core.Unauthenticated)
			return
		}
		s.respond(w, 200, object{"session": s.session(v, false)})
		return
	}
	model := "Empty"
	switch action {
	case "decision":
		model = "DecisionInput"
	case "consent":
		model = "ConsentReduction"
	case "poll":
		model = "PollInput"
	case "close":
		model = "CloseInput"
	case "call_result":
		model = "CallResultInput"
	}
	in, err := s.body(w, r, model)
	if err != nil {
		s.fail(w, err)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	switch action {
	case "register":
		v, agentToken, err := s.Core.Register(access)
		if err != nil {
			s.failAgent(w, err)
			return
		}
		s.respond(w, 201, object{"session": s.session(v, false), "agent_token": agentToken,
			"session_remaining_ms": v.RemainingMS, "decision_remaining_ms": v.DecisionRemainingMS})
	case "decision":
		mode, _ := in["mode"].(string)
		v, err := s.Core.Decide(sid, in["decision"].(string), mode)
		if err != nil {
			s.failAgent(w, err)
			return
		}
		out := object{"session": s.session(v, false)}
		if v.ActivationRemainingMS > 0 {
			out["activation_remaining_ms"] = v.ActivationRemainingMS
		}
		s.respond(w, 200, out)
	case "consent":
		v, err := s.Core.Reduce(sid, protocol.Integer(in["expected_revision"]), in["mode"].(string))
		if err == core.RevisionConflict {
			if current, e := s.Core.GetSession(sid); e == nil && current.Consent != nil {
				err = &core.Failure{Code: core.RevisionConflict, CurrentRevision: &current.Consent.Revision}
			}
		}
		if err != nil {
			s.failAgent(w, err)
			return
		}
		s.respond(w, 200, object{"session": s.session(v, false)})
	case "poll":
		acks := make([]string, 0, len(in["ack_event_ids"].([]any)))
		for _, value := range in["ack_event_ids"].([]any) {
			acks = append(acks, value.(string))
		}
		p, err := s.Core.BeginPoll(sid, protocol.Integer(in["consent_revision"]), acks)
		if err != nil {
			s.failAgent(w, err)
			return
		}
		defer s.Core.AbortPoll(p)
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-p.Ready:
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
		if r.Context().Err() != nil {
			return
		}
		if err = s.Core.AuthenticateAgent(sid, token); err != nil {
			s.fail(w, err)
			return
		}
		v, err := s.Core.CompletePoll(p)
		if err != nil {
			s.failAgent(w, err)
			return
		}
		s.respond(w, 200, s.poll(v))
	case "close":
		v, err := s.Core.End(sid, in["reason"].(string))
		if err != nil {
			s.failAgent(w, err)
			return
		}
		s.respond(w, 200, object{"session": s.session(v, false)})
	case "call_result":
		result := core.DiagnosticResult{ConsentRevision: protocol.Integer(in["consent_revision"])}
		if value, ok := in["error"].(map[string]any); ok {
			result.ErrorCode = value["code"].(string)
		} else if value, ok := in["result"].(map[string]any); ok {
			if pong, ok := value["pong"].(bool); ok {
				result.Pong = pong
				result.AgentTime, err = time.Parse(time.RFC3339Nano, value["agent_time"].(string))
			} else {
				info := &core.AgentInfo{Version: value["agent_version"].(string), OS: value["os"].(string), Arch: value["arch"].(string), Hostname: value["hostname"].(string)}
				for _, shell := range value["supported_shells"].([]any) {
					info.SupportedShells = append(info.SupportedShells, shell.(string))
				}
				result.Info = info
			}
		}
		if err == nil {
			err = s.Core.RecordDiagnostic(sid, id, result)
		}
		if err != nil {
			s.failAgent(w, err)
			return
		}
		w.WriteHeader(204)
	}
}

func (s *Server) poll(v core.PollResult) object {
	events := make([]object, 0, len(v.Events))
	for _, event := range v.Events {
		e := object{"id": event.ID, "type": event.Type, "session_id": event.SessionID, "created_at": timestamp(event.CreatedAt)}
		switch event.Type {
		case "session.end":
			e["payload"] = object{"reason": event.Reason, "ended_at": timestamp(*event.EndedAt)}
		case "task.cancel":
			e["payload"] = object{"task_id": event.TaskID, "reason": event.Reason, "confirmation_deadline": timestamp(*event.ConfirmationDeadline)}
		case "call.request":
			e["deadline"] = timestamp(*event.Deadline)
			e["payload"] = object{"call_id": event.CallID, "method": event.Method, "consent_revision": event.ConsentRevision}
		case "task.offer":
			t := event.Task
			e["deadline"] = timestamp(*event.Deadline)
			e["payload"] = object{"task": object{"id": t.ID, "session_id": t.SessionID, "request_id": t.RequestID, "phase": t.Phase,
				"consent_revision": t.ConsentRevision, "created_at": timestamp(t.CreatedAt), "offer_deadline": timestamp(t.OfferDeadline),
				"initiator": object{"credential_id": t.CredentialID, "mcp_client_id": t.ClientID}, "execution": object{"state": t.Execution.State},
				"details_available": true, "spec": object{"command": t.Spec.Command, "reason": t.Spec.Reason, "shell": t.Spec.Shell, "timeout_seconds": t.Spec.TimeoutSeconds}}}
		}
		events = append(events, e)
	}
	out := object{"session": s.session(v.Session, false), "session_remaining_ms": v.SessionRemainingMS, "events": events}
	if v.Lease != nil {
		out["lease"] = object{"seq": v.Lease.Seq, "consent_revision": v.Lease.ConsentRevision, "issued_at": timestamp(v.Lease.IssuedAt),
			"expires_at": timestamp(v.Lease.ExpiresAt), "valid_for_ms": v.Lease.ValidForMS}
	}
	return out
}
