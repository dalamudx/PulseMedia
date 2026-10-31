package httpapi

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mpBusinessClockApp struct{ AppContext }

func (mpBusinessClockApp) Clock() ports.Clock {
	return clock.NewMock(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
}

type seerrHTTPFixture struct{ calls int }

func (*seerrHTTPFixture) Configuration(context.Context, *ports.Actor) (ports.SeerrConfiguration, error) {
	return ports.SeerrConfiguration{}, nil
}
func (*seerrHTTPFixture) Save(context.Context, *ports.Actor, ports.SeerrConfigurationInput) (ports.SeerrConfiguration, error) {
	return ports.SeerrConfiguration{}, nil
}
func (*seerrHTTPFixture) Receipts(context.Context, *ports.Actor) ([]ports.SeerrReceipt, error) {
	return []ports.SeerrReceipt{}, nil
}
func (f *seerrHTTPFixture) Receive(_ context.Context, credential string, _ []byte) (ports.SeerrReceipt, error) {
	f.calls++
	if credential != strings.Repeat("dedicated-seerr-secret-", 2) {
		return ports.SeerrReceipt{}, ports.ErrUnauthorized
	}
	return ports.SeerrReceipt{ExternalRequestID: "42", RequestID: uuid.New(), ConfigVersion: 1, State: "pending", CreatedAt: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)}, nil
}
func TestSeerrDedicatedIngressAuthenticationBoundsAndNoStore(t *testing.T) {
	fixture := &seerrHTTPFixture{}
	s := &ServerHandler{app: mpBusinessClockApp{}, seerrSvc: fixture}
	fallback := 0
	handler := s.SeerrIngress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fallback++; w.WriteHeader(403) }))
	secret := strings.Repeat("dedicated-seerr-secret-", 2)
	for _, tc := range []struct {
		name, credential, body string
		duplicate              bool
		want                   int
	}{{"valid", secret, `{}`, false, 202}, {"generic-read-token", "Bearer generic-read-token", `{}`, false, 401}, {"wrong-dedicated", strings.Repeat("wrong-credential", 3), `{}`, false, 401}, {"duplicated-header", secret, `{}`, true, 401}, {"oversized", secret, strings.Repeat(" ", 65537), false, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/seerr", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", tc.credential)
			if tc.duplicate {
				r.Header.Add("Authorization", secret)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("secret response")
			}
		})
	}
	if fallback != 0 || fixture.calls != 2 {
		t.Fatal("generic auth route or unexpected I/O", fallback, fixture.calls)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/settings/seerr", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if fallback != 1 || w.Code != 403 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("protected route/no-store", w.Code)
	}
	for range 60 {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/seerr", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", secret)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
	}
	if w.Code != 429 {
		t.Fatal("rate bound not enforced", w.Code)
	}
}
func TestStrictMPBusinessJSONCookieAdmissionAndCanonicalFields(t *testing.T) {
	admin := mediaRequestFixtureActor(ports.RoleAdmin)
	token := *admin
	token.Scopes = []string{}
	for _, tc := range []struct {
		name, body string
		actor      *ports.Actor
		want       int
	}{{"valid", `{"enabled":false,"expected_version":0,"owner_id":"` + admin.ID + `"}`, admin, 204}, {"duplicate", `{"enabled":false,"enabled":true,"expected_version":0,"owner_id":"` + admin.ID + `"}`, admin, 400}, {"alias", `{"Enabled":true,"expected_version":0,"owner_id":"` + admin.ID + `"}`, admin, 400}, {"null", `{"enabled":null,"expected_version":0,"owner_id":"` + admin.ID + `"}`, admin, 400}, {"unknown", `{"enabled":false,"expected_version":0,"owner_id":"` + admin.ID + `","credential_url":"private"}`, admin, 400}, {"token", `{}`, &token, 403}, {"anonymous", `{}`, nil, 401}} {
		t.Run(tc.name, func(t *testing.T) {
			handler := StrictMPBusinessJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("restored body invalid")
				}
				w.WriteHeader(204)
			}))
			r := httptest.NewRequest(http.MethodPut, "/api/v1/settings/seerr", strings.NewReader(tc.body))
			if tc.actor != nil {
				r = r.WithContext(context.WithValue(r.Context(), actorKey, tc.actor))
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
