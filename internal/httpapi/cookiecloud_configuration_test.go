package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const cookieCloudTestBody = `{"expected_version":0,"enabled":true,"interval_seconds":1200,"connection":{"server_url":"https://cloud.example/private-prefix","user_key":"owned-user-key","password":"owned-password"}}`

type cookieCloudHTTPApplication struct {
	current                *ports.CookieCloudConfigView
	receipt                *ports.CookieCloudConfigReceipt
	err                    error
	reads, saves, receipts int
	input                  ports.CookieCloudConfigInput
}

func (f *cookieCloudHTTPApplication) Read(context.Context, *ports.Actor) (*ports.CookieCloudConfigView, error) {
	f.reads++
	return f.current, f.err
}
func (f *cookieCloudHTTPApplication) Save(_ context.Context, a *ports.Actor, in ports.CookieCloudConfigInput, key string) (*ports.CookieCloudConfigReceipt, error) {
	f.saves++
	f.input = in
	if f.err != nil {
		return nil, f.err
	}
	origin := "https://cloud.example"
	f.current = &ports.CookieCloudConfigView{OwnerID: uuid.MustParse(a.ID), Version: in.ExpectedVersion + 1, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, ServerOrigin: origin, HasConnection: true, UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	f.receipt = &ports.CookieCloudConfigReceipt{CommandKey: key, Config: *f.current}
	return f.receipt, nil
}
func (f *cookieCloudHTTPApplication) Receipt(context.Context, *ports.Actor, string) (*ports.CookieCloudConfigReceipt, error) {
	f.receipts++
	if f.err != nil {
		return nil, f.err
	}
	if f.receipt == nil {
		return nil, ports.ErrNotFound
	}
	return f.receipt, nil
}
func cookieCloudHTTPFixture(actor *ports.Actor) (http.Handler, *cookieCloudHTTPApplication) {
	app := &cookieCloudHTTPApplication{}
	s := &ServerHandler{}
	s.SetCookieCloudConfigService(app)
	generated := HandlerWithOptions(NewStrictHandler(s, nil), StdHTTPServerOptions{})
	admission := strictCookieCloudConfigJSON(generated)
	return cookieCloudConfigNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, actor))
		}
		admission.ServeHTTP(w, r)
	})), app
}
func cookieCloudHTTPCall(h http.Handler, method, path, body string, keys ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, key := range keys {
		r.Header.Add("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func cookieCloudHTTPAssert(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("unexpected status or cache policy", w.Code, w.Header().Get("Cache-Control"))
	}
	for _, secret := range []string{"owned-password", "owned-user-key", "private-prefix", "private-store-details"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("configuration response exposed a secret")
		}
	}
}

func TestCookieCloudConfigurationHTTPRoundTrip(t *testing.T) {
	h, app := cookieCloudHTTPFixture(checkinConfigAdmin())
	w := cookieCloudHTTPCall(h, http.MethodGet, cookieCloudConfigPath, "")
	cookieCloudHTTPAssert(t, w, http.StatusOK)
	if strings.TrimSpace(w.Body.String()) != `{"config":null}` {
		t.Fatal("missing config was not explicit null")
	}
	w = cookieCloudHTTPCall(h, http.MethodPut, cookieCloudConfigPath, cookieCloudTestBody, "original")
	cookieCloudHTTPAssert(t, w, http.StatusOK)
	if app.saves != 1 || app.input.Connection == nil || app.input.Connection.Password != "owned-password" || app.input.Connection.UserKey != "owned-user-key" {
		t.Fatal("formal generated input lost writeOnly fields")
	}
	w = cookieCloudHTTPCall(h, http.MethodGet, cookieCloudConfigPath+"/commands/original", "")
	cookieCloudHTTPAssert(t, w, http.StatusOK)
	if app.saves != 1 || app.receipts != 1 {
		t.Fatal("receipt GET attempted a save")
	}
	w = cookieCloudHTTPCall(h, http.MethodPut, cookieCloudConfigPath, `{"expected_version":1,"enabled":false,"interval_seconds":1200}`, "offline")
	cookieCloudHTTPAssert(t, w, http.StatusOK)
	if app.input.Connection != nil {
		t.Fatal("omitted connection did not preserve semantics")
	}
}

func TestCookieCloudConfigurationStrictJSONHTTP(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty", ""}, {"null", "null"}, {"array", "[]"},
		{"top-duplicate", strings.Replace(cookieCloudTestBody, `"enabled":true`, `"enabled":true,"enabled":false`, 1)},
		{"top-alias", strings.Replace(cookieCloudTestBody, `"enabled"`, `"Enabled"`, 1)},
		{"unknown", strings.Replace(cookieCloudTestBody, `"enabled":true`, `"enabled":true,"secret":"private"`, 1)},
		{"missing-enabled", strings.Replace(cookieCloudTestBody, `"enabled":true,`, "", 1)},
		{"null-enabled", strings.Replace(cookieCloudTestBody, `"enabled":true`, `"enabled":null`, 1)},
		{"null-connection", `{"expected_version":0,"enabled":false,"interval_seconds":1200,"connection":null}`},
		{"empty-connection", `{"expected_version":0,"enabled":false,"interval_seconds":1200,"connection":{}}`},
		{"nested-duplicate", strings.Replace(cookieCloudTestBody, `"password":"owned-password"`, `"password":"owned-password","password":"other"`, 1)},
		{"nested-alias", strings.Replace(cookieCloudTestBody, `"password"`, `"Password"`, 1)},
		{"nested-unknown", strings.Replace(cookieCloudTestBody, `"password":"owned-password"`, `"password":"owned-password","cookie":"private"`, 1)},
		{"nested-null", strings.Replace(cookieCloudTestBody, `"password":"owned-password"`, `"password":null`, 1)},
		{"fraction", strings.Replace(cookieCloudTestBody, `"interval_seconds":1200`, `"interval_seconds":1200.5`, 1)},
		{"string-number", strings.Replace(cookieCloudTestBody, `"expected_version":0`, `"expected_version":"0"`, 1)},
		{"version-overflow", strings.Replace(cookieCloudTestBody, `"expected_version":0`, `"expected_version":9223372036854775808`, 1)},
		{"version-final", strings.Replace(cookieCloudTestBody, `"expected_version":0`, `"expected_version":9223372036854775807`, 1)},
		{"trailing", cookieCloudTestBody + "{}"}, {"oversized", strings.Repeat(" ", 32769)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, app := cookieCloudHTTPFixture(checkinConfigAdmin())
			w := cookieCloudHTTPCall(h, http.MethodPut, cookieCloudConfigPath, tc.body, "original")
			cookieCloudHTTPAssert(t, w, http.StatusBadRequest)
			if app.saves+app.reads+app.receipts != 0 {
				t.Fatal("invalid body reached application")
			}
		})
	}
}

func TestCookieCloudConfigurationQueryAndCommandHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		keys                     []string
	}{
		{"query", http.MethodGet, cookieCloudConfigPath + "?private=yes", "", nil},
		{"empty-query", http.MethodGet, cookieCloudConfigPath + "?", "", nil},
		{"get-body", http.MethodGet, cookieCloudConfigPath, "{}", nil},
		{"get-key", http.MethodGet, cookieCloudConfigPath, "", []string{"key"}},
		{"receipt-body", http.MethodGet, cookieCloudConfigPath + "/commands/key", "{}", nil},
		{"receipt-key", http.MethodGet, cookieCloudConfigPath + "/commands/key", "", []string{"key"}},
		{"missing-write-key", http.MethodPut, cookieCloudConfigPath, cookieCloudTestBody, nil},
		{"duplicate-write-key", http.MethodPut, cookieCloudConfigPath, cookieCloudTestBody, []string{"key", "key"}},
		{"invalid-write-key", http.MethodPut, cookieCloudConfigPath, cookieCloudTestBody, []string{"bad key"}},
		{"long-write-key", http.MethodPut, cookieCloudConfigPath, cookieCloudTestBody, []string{strings.Repeat("x", 129)}},
		{"invalid-receipt-key", http.MethodGet, cookieCloudConfigPath + "/commands/key/more", "", nil},
		{"receipt-query", http.MethodGet, cookieCloudConfigPath + "/commands/key?private=yes", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, app := cookieCloudHTTPFixture(checkinConfigAdmin())
			w := cookieCloudHTTPCall(h, tc.method, tc.path, tc.body, tc.keys...)
			cookieCloudHTTPAssert(t, w, http.StatusBadRequest)
			if app.saves+app.reads+app.receipts != 0 {
				t.Fatal("invalid request reached application")
			}
		})
	}
}

func TestCookieCloudConfigurationSessionBoundaryHTTP(t *testing.T) {
	for _, kind := range []string{"anonymous", "member", "token", "system", "noncanonical"} {
		t.Run(kind, func(t *testing.T) {
			a := checkinConfigAdmin()
			want := http.StatusForbidden
			switch kind {
			case "anonymous":
				a = nil
				want = http.StatusUnauthorized
			case "member":
				a.Role = ports.RoleMember
			case "token":
				a.Scopes = []string{}
			case "system":
				a.Type = ports.ActorTypeSystem
			case "noncanonical":
				a.ID = "22222222222242228222222222222222"
			}
			h, app := cookieCloudHTTPFixture(a)
			for _, method := range []string{http.MethodGet, http.MethodPut} {
				w := cookieCloudHTTPCall(h, method, cookieCloudConfigPath, "not-json", "original")
				cookieCloudHTTPAssert(t, w, want)
			}
			if app.saves+app.reads+app.receipts != 0 {
				t.Fatal("unauthorized actor reached application")
			}
		})
	}
}

func TestCookieCloudConfigurationSafeErrorsHTTP(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{{"store", errors.New("private-store-details"), 503}, {"conflict", ports.ErrCookieCloudConfigConflict, 409}, {"forbidden", ports.ErrForbidden, 403}, {"missing", ports.ErrNotFound, 404}, {"rejected", ports.ErrRejected, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			h, app := cookieCloudHTTPFixture(checkinConfigAdmin())
			app.err = tc.err
			for _, request := range []struct {
				method, path, body string
				keys               []string
			}{{http.MethodGet, cookieCloudConfigPath, "", nil}, {http.MethodPut, cookieCloudConfigPath, cookieCloudTestBody, []string{"original"}}, {http.MethodGet, cookieCloudConfigPath + "/commands/original", "", nil}} {
				w := cookieCloudHTTPCall(h, request.method, request.path, request.body, request.keys...)
				cookieCloudHTTPAssert(t, w, tc.want)
			}
		})
	}
}
