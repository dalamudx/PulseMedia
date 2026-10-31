package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type managedHTTPFake struct {
	calls int
	err   error
}

func (f *managedHTTPFake) Preview(context.Context, *ports.Actor, uuid.UUID, uuid.UUID) (ports.ManagedSidecarUpdateResult, error) {
	f.calls++
	return ports.ManagedSidecarUpdateResult{State: "preview"}, f.err
}
func (f *managedHTTPFake) Command(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (ports.ManagedSidecarUpdateResult, error) {
	f.calls++
	return ports.ManagedSidecarUpdateResult{State: "completed"}, f.err
}
func (f *managedHTTPFake) ReadCommand(context.Context, *ports.Actor, uuid.UUID, string) (ports.ManagedSidecarUpdateResult, error) {
	f.calls++
	return ports.ManagedSidecarUpdateResult{State: "review_required"}, f.err
}
func managedHTTPRouter(f ManagedSidecarUpdateApplication, a *ports.Actor) http.Handler {
	m := http.NewServeMux()
	RegisterManagedSidecarUpdateRoutes(m, f)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, a))
		}
		m.ServeHTTP(w, r)
	})
}
func managedHTTPRequest(method, suffix, body string) *http.Request {
	r := httptest.NewRequest(method, "http://example.com/api/v1/library/files/"+tvUpgradeHTTPID+"/sidecar-update-"+suffix, strings.NewReader(body))
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "original-key")
	return r
}
func TestManagedSidecarHTTPRoutes(t *testing.T) {
	for _, tc := range []struct{ method, path, body, state string }{{"POST", "previews", `{"unit_id":"` + tvUpgradeHTTPID + `"}`, "preview"}, {"POST", "commands", `{"preview_id":"` + tvUpgradeHTTPID + `","confirmed":true}`, "completed"}, {"GET", "commands/original-key", "", "review_required"}} {
		t.Run(tc.path, func(t *testing.T) {
			f := &managedHTTPFake{}
			w := httptest.NewRecorder()
			managedHTTPRouter(f, tvUpgradeHTTPAdmin()).ServeHTTP(w, managedHTTPRequest(tc.method, tc.path, tc.body))
			if w.Code != 200 || f.calls != 1 || !strings.Contains(w.Body.String(), tc.state) || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("%d %s calls=%d", w.Code, w.Body, f.calls)
			}
		})
	}
}
func TestManagedSidecarHTTPRejectsMalformedConfirmation(t *testing.T) {
	for _, body := range []string{`{}`, `{"preview_id":"` + tvUpgradeHTTPID + `","confirmed":false}`, `{"preview_id":"` + tvUpgradeHTTPID + `","confirmed":true,"url":"https://bad"}`, `{"preview_id":"` + tvUpgradeHTTPID + `","confirmed":true,"confirmed":true}`, `{"preview_id":"` + tvUpgradeHTTPID + `","confirmed":true} {}`, `{"preview_id":null,"confirmed":true}`, `{"preview_id":"` + strings.ToUpper(tvUpgradeHTTPID) + `","confirmed":true}`, `{"preview_id":"` + tvUpgradeHTTPID + `","confirmed":"true"}`, `{"preview_id":"` + tvUpgradeHTTPID + `","confirmed":true,"path":"movie.nfo"}`} {
		t.Run(body, func(t *testing.T) {
			f := &managedHTTPFake{}
			w := httptest.NewRecorder()
			managedHTTPRouter(f, tvUpgradeHTTPAdmin()).ServeHTTP(w, managedHTTPRequest("POST", "commands", body))
			if w.Code != 400 || f.calls != 0 {
				t.Fatalf("%d %s calls=%d", w.Code, w.Body, f.calls)
			}
		})
	}
}
func TestManagedSidecarHTTPAdmission(t *testing.T) {
	for _, name := range []string{"anonymous", "member", "token", "cross-origin", "bad-key", "missing-key", "duplicate-key", "wrong-content-type", "unavailable", "stale"} {
		t.Run(name, func(t *testing.T) {
			a := tvUpgradeHTTPAdmin()
			f := &managedHTTPFake{}
			var app ManagedSidecarUpdateApplication = f
			r := managedHTTPRequest("POST", "commands", `{"preview_id":"`+tvUpgradeHTTPID+`","confirmed":true}`)
			want := 403
			switch name {
			case "anonymous":
				a = nil
				want = 401
			case "member":
				a.Role = "member"
			case "token":
				r.Header.Set("Authorization", "Bearer nope")
			case "cross-origin":
				r.Header.Set("Origin", "https://other.example")
			case "bad-key":
				r.Header.Set("Idempotency-Key", "bad/key")
				want = 400
			case "missing-key":
				r.Header.Del("Idempotency-Key")
				want = 400
			case "duplicate-key":
				r.Header.Add("Idempotency-Key", "second")
				want = 400
			case "wrong-content-type":
				r.Header.Set("Content-Type", "text/plain")
				want = 400
			case "unavailable":
				app = nil
				want = 503
			case "stale":
				f.err = ports.ErrMediaSidecarStale
				want = 409
			}
			w := httptest.NewRecorder()
			managedHTTPRouter(app, a).ServeHTTP(w, r)
			if w.Code != want || name != "stale" && f.calls != 0 {
				t.Fatalf("want %d got %d %s calls=%d", want, w.Code, w.Body, f.calls)
			}
		})
	}
}
