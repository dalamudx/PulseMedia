package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/auditlog"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/audit"
	"github.com/google/uuid"
)

type auditHTTPRead func(context.Context, *ports.Actor, auditlog.Filter, *auditlog.Position, int) ([]ports.AuditSummary, error)

func (f auditHTTPRead) ReadAuditTimeline(ctx context.Context, a *ports.Actor, q auditlog.Filter, p *auditlog.Position, n int) ([]ports.AuditSummary, error) {
	return f(ctx, a, q, p, n)
}
func auditHTTPActor() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleAdmin, ID: "00000000-0000-4000-8000-000000000001"}
}
func auditHTTPRouter(reader ports.AuditTimelineReader) http.Handler {
	h := &ServerHandler{}
	if reader != nil {
		h.SetAuditTimelineService(audit.NewTimelineService(reader))
	}
	return auditTimelineNoStore(ProblemResponseMiddleware()(strictAuditTimelineQuery(Handler(NewStrictHandler(h, nil)))))
}
func auditHTTPGet(t *testing.T, router http.Handler, a *ports.Actor, query string, status int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, auditTimelinePath+query, nil)
	if a != nil {
		request = request.WithContext(context.WithValue(request.Context(), actorKey, a))
	}
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	if result.Code != status || result.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status/cache: %d %v %s", result.Code, result.Header(), result.Body.String())
	}
	if status >= 400 && !strings.HasPrefix(result.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatal("error is not a problem response", result.Header())
	}
	return result
}

func TestAuditTimelineHTTPBindingAndContinuation(t *testing.T) {
	first := uuid.MustParse("00000000-0000-4000-8000-000000000009")
	second := uuid.MustParse("00000000-0000-4000-8000-000000000008")
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	calls := 0
	router := auditHTTPRouter(auditHTTPRead(func(_ context.Context, a *ports.Actor, f auditlog.Filter, before *auditlog.Position, limit int) ([]ports.AuditSummary, error) {
		calls++
		if a.ID != auditHTTPActor().ID || f.ActorType != "user" || f.ActorID != "owner" || f.Action != "literal_%'" || f.TargetType != "media" || f.TargetID != "作品" || !f.From.Equal(at.Add(-time.Hour)) || !f.Until.Equal(at.Add(time.Hour)) || limit != 2 {
			t.Fatal("query binding differs", f, limit)
		}
		row := ports.AuditSummary{ID: second, ActorType: "user", ActorID: "owner", Action: f.Action, TargetType: "media", TargetID: "作品", CreatedAt: at}
		if before != nil {
			if before.ID != first || !before.CreatedAt.Equal(at) {
				t.Fatal("cursor binding differs", before)
			}
			return []ports.AuditSummary{row}, nil
		}
		head := row
		head.ID = first
		return []ports.AuditSummary{head, row}, nil
	}))
	query := url.Values{"limit": {"1"}, "actor_type": {"user"}, "actor_id": {"owner"}, "action": {"literal_%'"}, "target_type": {"media"}, "target_id": {"作品"}, "from": {at.Add(-time.Hour).Format(time.RFC3339Nano)}, "until": {at.Add(time.Hour).Format(time.RFC3339Nano)}}
	response := auditHTTPGet(t, router, auditHTTPActor(), "?"+query.Encode(), http.StatusOK)
	var page AuditTimelinePage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Id != first || page.NextCursor == nil {
		t.Fatal("first summary", page)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw["items"], &items); err != nil {
		t.Fatal(err)
	}
	if len(items[0]) != 7 || items[0]["payload"] != nil {
		t.Fatal("unexpected historical payload projection")
	}
	query.Set("cursor", *page.NextCursor)
	response = auditHTTPGet(t, router, auditHTTPActor(), "?"+query.Encode(), http.StatusOK)
	page = AuditTimelinePage{}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Id != second || page.NextCursor != nil || calls != 2 {
		t.Fatal("second page", page, calls)
	}
	foreign := auditHTTPActor()
	foreign.ID = "00000000-0000-4000-8000-000000000002"
	auditHTTPGet(t, router, foreign, "?"+query.Encode(), http.StatusBadRequest)
	if calls != 2 {
		t.Fatal("foreign cursor reached repository")
	}
}

func TestAuditTimelineHTTPAuthorizationBeforeQuery(t *testing.T) {
	calls := 0
	router := auditHTTPRouter(auditHTTPRead(func(context.Context, *ports.Actor, auditlog.Filter, *auditlog.Position, int) ([]ports.AuditSummary, error) {
		calls++
		return nil, nil
	}))
	auditHTTPGet(t, router, nil, "?limit=0", http.StatusUnauthorized)
	for _, actor := range []*ports.Actor{{Type: ports.ActorTypeUser, ID: auditHTTPActor().ID, Role: ports.RoleMember}, {Type: ports.ActorTypeSystem, ID: auditHTTPActor().ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: auditHTTPActor().ID, Role: ports.RoleAdmin, Scopes: []string{}}, {Type: ports.ActorTypeUser, ID: auditHTTPActor().ID, Role: ports.RoleAdmin, Scopes: []string{"admin"}}} {
		auditHTTPGet(t, router, actor, "?limit=0", http.StatusForbidden)
	}
	if calls != 0 {
		t.Fatal("denied request accessed storage")
	}
	token := auditHTTPActor()
	token.Scopes = []string{"read"}
	response := auditHTTPGet(t, router, token, "", http.StatusOK)
	if !strings.Contains(response.Body.String(), `"items":[]`) || calls != 1 {
		t.Fatal("valid read token or empty list", response.Body.String())
	}
	auditHTTPGet(t, auditHTTPRouter(nil), auditHTTPActor(), "", http.StatusServiceUnavailable)
}

func TestAuditTimelineHTTPStrictQuery(t *testing.T) {
	calls := 0
	router := auditHTTPRouter(auditHTTPRead(func(context.Context, *ports.Actor, auditlog.Filter, *auditlog.Position, int) ([]ports.AuditSummary, error) {
		calls++
		return nil, nil
	}))
	queries := []string{"?unknown=1", "?Limit=1", "?limit=1&limit=2", "?limit=0", "?limit=-1", "?limit=201", "?limit=01", "?limit=%2B1", "?limit=1.0", "?cursor=", "?cursor=broken", "?actor_type=admin", "?actor_id=", "?action=%0A", "?action=%FF", "?target_type=%20media", "?from=not-a-time", "?from=0001-01-01T00:00:00Z", "?from=2026-10-23T00:00:00.0000001Z", "?from=2026-10-24T00:00:00Z&until=2026-10-23T00:00:00Z", "?until=2026-10-23T00:00:00Z&until=2026-10-24T00:00:00Z", "?action=bad;target_id=a", "?action=%zz", "?cursor=" + strings.Repeat("a", 1025), "?action=" + strings.Repeat("a", 257), "?action=" + strings.Repeat("a", 8193)}
	for _, query := range queries {
		auditHTTPGet(t, router, auditHTTPActor(), query, http.StatusBadRequest)
	}
	if calls != 0 {
		t.Fatal("invalid query reached storage", calls)
	}
}

func TestAuditTimelineHTTPFailureIsNotEmptySuccess(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{ports.ErrForbidden, http.StatusForbidden}, {ports.ErrUnauthorized, http.StatusUnauthorized}, {ports.ErrRejected, http.StatusBadRequest}, {errors.New("private-pgx-cookie-passkey"), http.StatusServiceUnavailable}, {context.DeadlineExceeded, http.StatusServiceUnavailable}} {
		router := auditHTTPRouter(auditHTTPRead(func(context.Context, *ports.Actor, auditlog.Filter, *auditlog.Position, int) ([]ports.AuditSummary, error) {
			return nil, tc.err
		}))
		response := auditHTTPGet(t, router, auditHTTPActor(), "", tc.status)
		for _, secret := range []string{"private-pgx-cookie-passkey", "SQLSTATE", `"items"`} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatal("failure leaked internal data or success")
			}
		}
	}
}
