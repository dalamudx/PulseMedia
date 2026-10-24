package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const seedHTTPPath = seedingConfigPrefix + "22222222-2222-4222-8222-222222222222/seeding/config"
const seedHTTPBody = `{"expected_version":0,"expected_client_updated_at":"2026-01-02T03:04:05Z","enabled":true,"interval_seconds":1200,"rule":{"mode":"any","delete_files":false,"min_share_ratio":2}}`

type seedHTTPApplication struct {
	current                *ports.SeedingConfigView
	receipt                *ports.SeedingConfigReceipt
	err                    error
	reads, saves, receipts int
	input                  ports.SeedingConfigInput
	client                 uuid.UUID
}

func (f *seedHTTPApplication) Read(_ context.Context, _ *ports.Actor, id uuid.UUID) (*ports.SeedingConfigView, error) {
	f.reads++
	f.client = id
	return f.current, f.err
}
func (f *seedHTTPApplication) Save(_ context.Context, a *ports.Actor, id uuid.UUID, in ports.SeedingConfigInput, key string) (*ports.SeedingConfigReceipt, error) {
	f.saves++
	f.client = id
	f.input = in
	if f.err != nil {
		return nil, f.err
	}
	f.current = &ports.SeedingConfigView{ClientID: id, OwnerID: uuid.MustParse(a.ID), Version: in.ExpectedVersion + 1, ClientUpdatedAt: in.ExpectedClientUpdatedAt, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, Rule: in.Rule, UpdatedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)}
	f.receipt = &ports.SeedingConfigReceipt{CommandKey: key, Config: *f.current}
	return f.receipt, nil
}
func (f *seedHTTPApplication) Receipt(_ context.Context, _ *ports.Actor, id uuid.UUID, _ string) (*ports.SeedingConfigReceipt, error) {
	f.receipts++
	f.client = id
	if f.err != nil {
		return nil, f.err
	}
	if f.receipt == nil {
		return nil, ports.ErrNotFound
	}
	return f.receipt, nil
}
func seedHTTPFixture(actor *ports.Actor) (http.Handler, *seedHTTPApplication) {
	app := &seedHTTPApplication{}
	s := &ServerHandler{}
	s.SetSeedingConfigService(app)
	generated := HandlerWithOptions(NewStrictHandler(s, nil), StdHTTPServerOptions{})
	strict := strictSeedingConfigJSON(generated)
	return seedingConfigNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, actor))
		}
		strict.ServeHTTP(w, r)
	})), app
}
func TestSeedingConfigurationHTTPRoundTrip(t *testing.T) {
	h, app := seedHTTPFixture(checkinConfigAdmin())
	w := cookieCloudHTTPCall(h, http.MethodGet, seedHTTPPath, "")
	cookieCloudHTTPAssert(t, w, http.StatusOK)
	if strings.TrimSpace(w.Body.String()) != `{"config":null}` {
		t.Fatal("absence was not explicit")
	}
	saved := cookieCloudHTTPCall(h, http.MethodPut, seedHTTPPath, seedHTTPBody, "original")
	cookieCloudHTTPAssert(t, saved, http.StatusOK)
	if app.saves != 1 || app.client.String() != "22222222-2222-4222-8222-222222222222" || app.input.Rule.Mode != rule.MatchAny || app.input.Rule.MinShareRatio == nil || *app.input.Rule.MinShareRatio != 2 || app.input.Rule.DeleteFiles {
		t.Fatal("generated rule or scope lost")
	}
	receipt := cookieCloudHTTPCall(h, http.MethodGet, seedHTTPPath+"/commands/original", "")
	cookieCloudHTTPAssert(t, receipt, http.StatusOK)
	if receipt.Body.String() != saved.Body.String() || app.saves != 1 || app.receipts != 1 {
		t.Fatal("receipt read attempted save or changed result")
	}
}
func TestSeedingConfigurationStrictJSONHTTP(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty", ""}, {"null", "null"}, {"array", "[]"},
		{"top-duplicate", strings.Replace(seedHTTPBody, `"enabled":true`, `"enabled":true,"enabled":false`, 1)},
		{"top-alias", strings.Replace(seedHTTPBody, `"enabled"`, `"Enabled"`, 1)},
		{"unknown", strings.Replace(seedHTTPBody, `"enabled":true`, `"enabled":true,"secret":"private"`, 1)},
		{"missing-enabled", strings.Replace(seedHTTPBody, `"enabled":true,`, "", 1)},
		{"null-enabled", strings.Replace(seedHTTPBody, `"enabled":true`, `"enabled":null`, 1)},
		{"null-version", strings.Replace(seedHTTPBody, `"expected_version":0`, `"expected_version":null`, 1)},
		{"bad-date", strings.Replace(seedHTTPBody, "2026-01-02T03:04:05Z", "2026-01-02", 1)},
		{"zero-date", strings.Replace(seedHTTPBody, "2026-01-02T03:04:05Z", "0001-01-01T00:00:00Z", 1)},
		{"null-rule", strings.Replace(seedHTTPBody, `{"mode":"any","delete_files":false,"min_share_ratio":2}`, `null`, 1)},
		{"array-rule", strings.Replace(seedHTTPBody, `{"mode":"any","delete_files":false,"min_share_ratio":2}`, `[]`, 1)},
		{"nested-duplicate", strings.Replace(seedHTTPBody, `"min_share_ratio":2`, `"min_share_ratio":2,"min_share_ratio":3`, 1)},
		{"nested-alias", strings.Replace(seedHTTPBody, `"min_share_ratio"`, `"Min_share_ratio"`, 1)},
		{"nested-unknown", strings.Replace(seedHTTPBody, `"min_share_ratio":2`, `"min_share_ratio":2,"keep_files":true`, 1)},
		{"nested-null", strings.Replace(seedHTTPBody, `"min_share_ratio":2`, `"min_share_ratio":null`, 1)},
		{"null-delete", strings.Replace(seedHTTPBody, `"delete_files":false`, `"delete_files":null`, 1)},
		{"delete-data", strings.Replace(seedHTTPBody, `"delete_files":false`, `"delete_files":true`, 1)},
		{"missing-delete", strings.Replace(seedHTTPBody, `"delete_files":false,`, "", 1)},
		{"missing-criteria", strings.Replace(seedHTTPBody, `,"min_share_ratio":2`, "", 1)},
		{"unknown-mode", strings.Replace(seedHTTPBody, `"mode":"any"`, `"mode":"guess"`, 1)},
		{"ratio-zero", strings.Replace(seedHTTPBody, `"min_share_ratio":2`, `"min_share_ratio":0`, 1)},
		{"ratio-too-large", strings.Replace(seedHTTPBody, `"min_share_ratio":2`, `"min_share_ratio":1000001`, 1)},
		{"fractional-seconds", strings.Replace(seedHTTPBody, `"min_share_ratio":2`, `"min_seeding_seconds":1.5`, 1)},
		{"zero-space", strings.Replace(seedHTTPBody, `"min_share_ratio":2`, `"low_free_bytes":0`, 1)},
		{"integer-overflow", strings.Replace(seedHTTPBody, `"min_share_ratio":2`, `"low_free_bytes":9223372036854775808`, 1)},
		{"fractional-interval", strings.Replace(seedHTTPBody, `"interval_seconds":1200`, `"interval_seconds":1200.5`, 1)},
		{"final-version", strings.Replace(seedHTTPBody, `"expected_version":0`, `"expected_version":9223372036854775807`, 1)},
		{"trailing", seedHTTPBody + "{}"}, {"oversized", strings.Repeat(" ", 32769)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, app := seedHTTPFixture(checkinConfigAdmin())
			w := cookieCloudHTTPCall(h, http.MethodPut, seedHTTPPath, tc.body, "original")
			cookieCloudHTTPAssert(t, w, http.StatusBadRequest)
			if app.saves+app.reads+app.receipts != 0 {
				t.Fatal("invalid body reached application")
			}
		})
	}
}
func TestSeedingConfigurationQueryAndCommandHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		keys                     []string
	}{
		{"query", http.MethodGet, seedHTTPPath + "?private=yes", "", nil}, {"empty-query", http.MethodGet, seedHTTPPath + "?", "", nil},
		{"get-body", http.MethodGet, seedHTTPPath, "{}", nil}, {"get-key", http.MethodGet, seedHTTPPath, "", []string{"key"}},
		{"receipt-body", http.MethodGet, seedHTTPPath + "/commands/key", "{}", nil}, {"receipt-key", http.MethodGet, seedHTTPPath + "/commands/key", "", []string{"key"}},
		{"missing-key", http.MethodPut, seedHTTPPath, seedHTTPBody, nil}, {"duplicate-key", http.MethodPut, seedHTTPPath, seedHTTPBody, []string{"key", "key"}},
		{"invalid-key", http.MethodPut, seedHTTPPath, seedHTTPBody, []string{"bad key"}}, {"long-key", http.MethodPut, seedHTTPPath, seedHTTPBody, []string{strings.Repeat("x", 129)}},
		{"bad-receipt", http.MethodGet, seedHTTPPath + "/commands/key/more", "", nil}, {"receipt-query", http.MethodGet, seedHTTPPath + "/commands/key?private=yes", "", nil},
		{"nil-id", http.MethodGet, strings.Replace(seedHTTPPath, "22222222-2222-4222-8222-222222222222", uuid.Nil.String(), 1), "", nil},
		{"noncanonical-id", http.MethodGet, strings.Replace(seedHTTPPath, "22222222-2222-4222-8222-222222222222", "22222222222242228222222222222222", 1), "", nil},
		{"encoded-id", http.MethodGet, strings.Replace(seedHTTPPath, "22222222-2222", "%322222222-2222", 1), "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, app := seedHTTPFixture(checkinConfigAdmin())
			w := cookieCloudHTTPCall(h, tc.method, tc.path, tc.body, tc.keys...)
			cookieCloudHTTPAssert(t, w, http.StatusBadRequest)
			if app.saves+app.reads+app.receipts != 0 {
				t.Fatal("invalid request reached application")
			}
		})
	}
}
func TestSeedingConfigurationSessionBoundaryHTTP(t *testing.T) {
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
			h, app := seedHTTPFixture(a)
			for _, method := range []string{http.MethodGet, http.MethodPut} {
				w := cookieCloudHTTPCall(h, method, seedHTTPPath, "not-json", "original")
				cookieCloudHTTPAssert(t, w, want)
			}
			if app.saves+app.reads+app.receipts != 0 {
				t.Fatal("unauthorized actor reached application")
			}
		})
	}
}
func TestSeedingConfigurationSafeErrorsHTTP(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{{"store", errors.New("private-store-details"), http.StatusServiceUnavailable}, {"conflict", ports.ErrSeedingConfigConflict, http.StatusConflict}, {"forbidden", ports.ErrForbidden, http.StatusForbidden}, {"missing", ports.ErrNotFound, http.StatusNotFound}, {"rejected", ports.ErrRejected, http.StatusBadRequest}} {
		t.Run(tc.name, func(t *testing.T) {
			h, app := seedHTTPFixture(checkinConfigAdmin())
			app.err = tc.err
			for _, r := range []struct {
				method, path, body string
				keys               []string
			}{{http.MethodGet, seedHTTPPath, "", nil}, {http.MethodPut, seedHTTPPath, seedHTTPBody, []string{"original"}}, {http.MethodGet, seedHTTPPath + "/commands/original", "", nil}} {
				w := cookieCloudHTTPCall(h, r.method, r.path, r.body, r.keys...)
				cookieCloudHTTPAssert(t, w, tc.want)
			}
		})
	}
}
