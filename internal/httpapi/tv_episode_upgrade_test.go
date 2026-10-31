package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const tvUpgradeHTTPID = "ab123456-1234-4123-8123-123456789abc"
const tvUpgradeHTTPPreview = `{"unit_id":"` + tvUpgradeHTTPID + `","current_file_id":"` + tvUpgradeHTTPID + `","candidate_id":"` + tvUpgradeHTTPID + `"}`

func tvUpgradeHTTPCommand(action string) string {
	body := strings.TrimSuffix(tvUpgradeHTTPPreview, "}") + `,"action":"` + action + `","expected_version":"` + strings.Repeat("a", 64) + `","confirmed":true`
	if action != "reserve" {
		body += `,"operation_id":"` + tvUpgradeHTTPID + `"`
	}
	return body + `}`
}

type tvUpgradeHTTPFake struct {
	calls        []string
	command      ports.TVEpisodeUpgradeCommand
	subscription uuid.UUID
	key          string
	err          error
}

func (f *tvUpgradeHTTPFake) Preview(_ context.Context, _ *ports.Actor, r ports.TVEpisodeUpgradePreviewRequest) (*ports.TVEpisodeUpgradeResult, error) {
	f.calls = append(f.calls, "preview")
	f.subscription = r.SubscriptionID
	return &ports.TVEpisodeUpgradeResult{Status: "preview_not_executable", UnitID: r.UnitID}, f.err
}
func (f *tvUpgradeHTTPFake) Command(_ context.Context, _ *ports.Actor, c ports.TVEpisodeUpgradeCommand, key string) (*ports.TVEpisodeUpgradeResult, error) {
	f.calls = append(f.calls, c.Action)
	f.command = c
	f.subscription = c.SubscriptionID
	f.key = key
	return &ports.TVEpisodeUpgradeResult{Status: "reserved_not_executable"}, f.err
}
func (f *tvUpgradeHTTPFake) ReadCommand(_ context.Context, _ *ports.Actor, id uuid.UUID, key string) (*ports.TVEpisodeUpgradeResult, error) {
	f.calls = append(f.calls, "read")
	f.subscription = id
	f.key = key
	return &ports.TVEpisodeUpgradeResult{Status: "reserved_not_executable"}, f.err
}
func tvUpgradeHTTPRouter(f TVEpisodeUpgradeApplication, actor *ports.Actor) http.Handler {
	mux := http.NewServeMux()
	RegisterTVEpisodeUpgradeRoutes(mux, f)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, actor))
		}
		mux.ServeHTTP(w, r)
	})
}
func tvUpgradeHTTPAdmin() *ports.Actor {
	return &ports.Actor{ID: tvUpgradeHTTPID, Type: ports.ActorTypeUser, Role: "admin"}
}
func tvUpgradeHTTPRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "http://example.com/api/v1/subscriptions/"+tvUpgradeHTTPID+"/tv-episode-upgrade-"+path, strings.NewReader(body))
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "original-key")
	return r
}
func TestTVEpisodeUpgradeHTTPRoutes(t *testing.T) {
	f := &tvUpgradeHTTPFake{}
	router := tvUpgradeHTTPRouter(f, tvUpgradeHTTPAdmin())
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "previews", tvUpgradeHTTPPreview, 200}, {"POST", "commands", tvUpgradeHTTPCommand("reserve"), 202}, {"GET", "commands/original-key", "", 200},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, tvUpgradeHTTPRequest(tc.method, tc.path, tc.body))
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	if strings.Join(f.calls, ",") != "preview,reserve,read" || f.subscription.String() != tvUpgradeHTTPID || f.key != "original-key" || !f.command.Confirmed {
		t.Fatalf("incorrect dispatch: %+v", f)
	}
	for _, action := range []string{"prepare", "send", "observe", "organization-preview", "organization-confirm", "organize", "retirement-preview", "retirement-confirm", "retire"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, tvUpgradeHTTPRequest("POST", "commands", tvUpgradeHTTPCommand(action)))
		if w.Code != 202 || f.command.Action != action || f.command.OperationID.String() != tvUpgradeHTTPID {
			t.Fatalf("%s: %d", action, w.Code)
		}
	}
}
func TestTVEpisodeUpgradeHTTPRejectsMalformedJSON(t *testing.T) {
	valid := tvUpgradeHTTPCommand("reserve")
	for name, body := range map[string]string{
		"unknown":           strings.TrimSuffix(valid, "}") + `,"path":"/unsafe"}`,
		"duplicate":         strings.TrimSuffix(valid, "}") + `,"confirmed":true}`,
		"escaped-duplicate": strings.TrimSuffix(valid, "}") + `,"confi\u0072med":true}`,
		"trailing":          valid + ` {}`,
		"uppercase-uuid":    strings.Replace(valid, tvUpgradeHTTPID, strings.ToUpper(tvUpgradeHTTPID), 1),
		"compact-uuid":      strings.Replace(valid, tvUpgradeHTTPID, strings.ReplaceAll(tvUpgradeHTTPID, "-", ""), 1),
		"zero-uuid":         strings.Replace(valid, tvUpgradeHTTPID, uuid.Nil.String(), 1),
		"uppercase-version": strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"short-version":     strings.Replace(valid, strings.Repeat("a", 64), "a", 1),
		"null":              strings.Replace(valid, `"confirmed":true`, `"confirmed":null`, 1),
		"unconfirmed":       strings.Replace(valid, `"confirmed":true`, `"confirmed":false`, 1),
		"missing-operation": strings.Replace(valid, `"reserve"`, `"send"`, 1),
		"reserve-operation": strings.TrimSuffix(valid, "}") + `,"operation_id":"` + tvUpgradeHTTPID + `"}`,
		"unknown-action":    strings.Replace(valid, `"reserve"`, `"delete"`, 1),
		"oversized":         valid + strings.Repeat(" ", 8<<10),
	} {
		t.Run(name, func(t *testing.T) {
			f := &tvUpgradeHTTPFake{}
			w := httptest.NewRecorder()
			tvUpgradeHTTPRouter(f, tvUpgradeHTTPAdmin()).ServeHTTP(w, tvUpgradeHTTPRequest("POST", "commands", body))
			if w.Code != 400 || len(f.calls) != 0 {
				t.Fatalf("code=%d calls=%v", w.Code, f.calls)
			}
		})
	}
}
func TestTVEpisodeUpgradeHTTPAdmission(t *testing.T) {
	for _, name := range []string{"anonymous", "member", "system", "token", "bearer", "cross-origin", "no-origin", "multiple-origins", "bad-subscription", "no-key", "duplicate-key", "invalid-key", "wrong-content-type"} {
		t.Run(name, func(t *testing.T) {
			a := tvUpgradeHTTPAdmin()
			r := tvUpgradeHTTPRequest("POST", "commands", tvUpgradeHTTPCommand("reserve"))
			want := 403
			switch name {
			case "anonymous":
				a = nil
				want = 401
			case "member":
				a.Role = "member"
			case "system":
				a.Type = ports.ActorTypeSystem
			case "token":
				a.Scopes = []string{}
			case "bearer":
				r.Header.Set("Authorization", "Bearer token")
			case "cross-origin":
				r.Header.Set("Origin", "http://other.example")
			case "no-origin":
				r.Header.Del("Origin")
			case "multiple-origins":
				r.Header.Add("Origin", "http://example.com")
			case "bad-subscription":
				r.URL.Path = strings.Replace(r.URL.Path, tvUpgradeHTTPID, strings.ToUpper(tvUpgradeHTTPID), 1)
				want = 400
			case "no-key":
				r.Header.Del("Idempotency-Key")
				want = 400
			case "duplicate-key":
				r.Header.Add("Idempotency-Key", "second")
				want = 400
			case "invalid-key":
				r.Header.Set("Idempotency-Key", "bad key")
				want = 400
			case "wrong-content-type":
				r.Header.Set("Content-Type", "text/plain")
				want = 400
			}
			f := &tvUpgradeHTTPFake{}
			w := httptest.NewRecorder()
			tvUpgradeHTTPRouter(f, a).ServeHTTP(w, r)
			if w.Code != want || len(f.calls) != 0 || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("code=%d want=%d calls=%v", w.Code, want, f.calls)
			}
		})
	}
}
func TestTVEpisodeUpgradeHTTPErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{ports.ErrTVEpisodeUpgradeConflict, 409}, {ports.ErrTVEpisodeUpgradeObservation, 409}, {ports.ErrNotFound, 404}, {ports.ErrForbidden, 403}, {errors.New("secret native URL"), 503},
	} {
		f := &tvUpgradeHTTPFake{err: tc.err}
		w := httptest.NewRecorder()
		tvUpgradeHTTPRouter(f, tvUpgradeHTTPAdmin()).ServeHTTP(w, tvUpgradeHTTPRequest("GET", "commands/original-key", ""))
		if w.Code != tc.status || strings.Contains(w.Body.String(), "secret native URL") || strings.Join(f.calls, ",") != "read" {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	tvUpgradeHTTPRouter(nil, tvUpgradeHTTPAdmin()).ServeHTTP(w, tvUpgradeHTTPRequest("POST", "previews", tvUpgradeHTTPPreview))
	if w.Code != 503 {
		t.Fatalf("nil service: %d", w.Code)
	}
}
