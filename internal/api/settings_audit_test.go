package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/api/settingstest"
	"github.com/TechDufus/dash/internal/model"
)

func (e *settingsEnv) auditPage(t *testing.T, query string) model.SettingsAudit {
	t.Helper()
	w := e.do(http.MethodGet, "/api/v1/settings/audit"+query, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("audit%s = %d %s", query, w.Code, w.Body.String())
	}
	return decodeBody[model.SettingsAudit](t, w)
}

// The audit view pages one ring at a time, newest first, with a cursor that belongs to its ring.
func TestSettingsAuditPagesARingWithItsOwnCursor(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{})
	for _, node := range []string{"a", "b", "c"} {
		wantOK(t, e.put(proxmoxPath, map[string]any{"node": node}))
	}
	viewer := req{method: http.MethodGet, path: "/api/v1/settings", header: map[string]string{"X-Forwarded-Preferred-Username": "bob"}}
	if w := viewer.do(e.h); w.Code != http.StatusForbidden {
		t.Fatalf("viewer = %d", w.Code)
	}

	first := e.auditPage(t, "?limit=4")
	if first.Ring != "main" || first.Source != "memory" || first.Enabled || len(first.Records) != 4 || !strings.HasPrefix(first.Next, "main.") {
		t.Fatalf("first page = %+v", first)
	}
	r0 := first.Records[0]
	if r0.Kind != "outcome" || r0.Action != actionSettingsSet || r0.Result != "ok" || r0.Actor.User != "alice" ||
		r0.Target == nil || r0.Target.Name != "proxmox" || len(r0.Fields) != 1 || r0.Fields[0] != "plugins.proxmox.node" {
		t.Errorf("newest record = %+v", r0)
	}
	second := e.auditPage(t, "?limit=4&before="+first.Next)
	if len(second.Records) != 2 || second.Next != "" || second.Records[1].Kind != "intent" {
		t.Errorf("second page = %+v", second)
	}
	unauth := e.auditPage(t, "?ring=unauth")
	if unauth.Ring != "unauth" || len(unauth.Records) != 1 || unauth.Records[0].Kind != "denial" || unauth.Records[0].Actor.User != "bob" {
		t.Errorf("unauth = %+v", unauth)
	}
	if all := e.auditPage(t, "?limit=100000"); len(all.Records) != 6 {
		t.Errorf("a large limit lists %d", len(all.Records))
	}
	for _, q := range []string{"?ring=unauth&before=" + first.Next, "?before=" + strings.TrimPrefix(first.Next, "main."), "?ring=all", "?limit=0", "?limit=x"} {
		wantErr(t, e.do(http.MethodGet, "/api/v1/settings/audit"+q, nil, nil), http.StatusBadRequest, model.ErrCodeInvalidInput)
	}
}
