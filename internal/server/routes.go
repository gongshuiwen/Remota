package server

import "strings"

// These declarations drive actual dispatch. Tests compare them bidirectionally
// with OpenAPI; adding a handler without documenting it cannot pass that check.
type httpRoute struct {
	Path, Method, Action string
}

var adminRoutes = []httpRoute{
	{"/healthz", "GET", "health"},
	{"/api/v1/admin/login", "POST", "login"},
	{"/api/v1/admin/logout", "POST", "logout"},
	{"/api/v1/admin/state", "GET", "state"},
	{"/api/v1/admin/credentials", "POST", "create"},
	{"/api/v1/admin/credentials/{cid}/rotate", "POST", "rotate"},
	{"/api/v1/admin/credentials/{cid}/revoke", "POST", "revoke"},
	{"/api/v1/admin/sessions/{sid}", "PATCH", "note"},
	{"/api/v1/admin/sessions/{sid}/end", "POST", "end_session"},
	{"/api/v1/admin/clients/{mcl_id}/end", "POST", "end_client"},
}

var agentRoutes = []httpRoute{
	{"/api/v1/agent/sessions", "POST", "register"},
	{"/api/v1/agent/sessions/{sid}", "GET", "get"},
	{"/api/v1/agent/sessions/{sid}/decision", "PUT", "decision"},
	{"/api/v1/agent/sessions/{sid}/consent", "PUT", "consent"},
	{"/api/v1/agent/sessions/{sid}/poll", "POST", "poll"},
	{"/api/v1/agent/sessions/{sid}/close", "POST", "close"},
	{"/api/v1/agent/sessions/{sid}/calls/{call_id}/result", "POST", "call_result"},
	{"/api/v1/agent/sessions/{sid}/tasks/{tid}/start", "POST", "task_start"},
	{"/api/v1/agent/sessions/{sid}/tasks/{tid}/receipts", "POST", "task_receipts"},
	{"/api/v1/agent/sessions/{sid}/tasks/{tid}/result", "POST", "task_result"},
}

func matchRoute(routes []httpRoute, path string) (httpRoute, map[string]string) {
	parts := strings.Split(path, "/")
	for _, candidate := range routes {
		template := strings.Split(candidate.Path, "/")
		if len(template) != len(parts) {
			continue
		}
		params := make(map[string]string)
		matched := true
		for i, part := range template {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				if parts[i] == "" {
					matched = false
					break
				}
				params[part[1:len(part)-1]] = parts[i]
			} else if part != parts[i] {
				matched = false
				break
			}
		}
		if matched {
			return candidate, params
		}
	}
	return httpRoute{}, nil
}
