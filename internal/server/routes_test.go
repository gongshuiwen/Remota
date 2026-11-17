package server

import (
	"strings"
	"testing"

	"remota.local/remota/internal/contracttest"
)

func TestOpenAPIRoutes(t *testing.T) {
	contract, err := contracttest.Load()
	must(t, err)
	documented := contract.Operations()
	implemented := make(map[string]bool)
	for _, group := range [][]httpRoute{adminRoutes, agentRoutes} {
		for _, entry := range group {
			key := entry.Method + " " + entry.Path
			if implemented[key] || !documented[key] {
				t.Errorf("duplicate or undocumented route: %s", key)
			}
			implemented[key] = true
			path := strings.NewReplacer("{sid}", "session", "{tid}", "task", "{cid}", "credential",
				"{mcl_id}", "client", "{call_id}", "call").Replace(entry.Path)
			if strings.HasPrefix(path, "/api/v1/agent/") {
				action, sid, id, method := agentRoute(path)
				if action != entry.Action || method != entry.Method || strings.Contains(entry.Path, "{sid}") && sid != "session" ||
					strings.Contains(entry.Path, "{tid}") && id != "task" || strings.Contains(entry.Path, "{call_id}") && id != "call" {
					t.Errorf("agent parameter extraction differs: %s", key)
				}
			} else {
				action, id, method := route(path)
				if action != entry.Action || method != entry.Method || strings.Contains(entry.Path, "{cid}") && id != "credential" ||
					strings.Contains(entry.Path, "{sid}") && id != "session" || strings.Contains(entry.Path, "{mcl_id}") && id != "client" {
					t.Errorf("admin parameter extraction differs: %s", key)
				}
			}
			if found, _ := matchRoute(group, path+"/"); found.Action != "" {
				t.Errorf("accepted undeclared trailing slash: %s/", path)
			}
			if strings.Contains(entry.Path, "{") {
				empty := strings.NewReplacer("{sid}", "", "{tid}", "", "{cid}", "", "{mcl_id}", "", "{call_id}", "").Replace(entry.Path)
				if found, _ := matchRoute(group, empty); found.Action != "" {
					t.Errorf("accepted an empty path parameter: %s", empty)
				}
			}
		}
	}
	for key := range documented {
		if !implemented[key] {
			t.Errorf("documented route has no implementation: %s", key)
		}
	}
}
