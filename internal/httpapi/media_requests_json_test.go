package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const mediaRequestTestID = "01960000-0000-7000-8000-000000000001"

func mediaRequestFixtureActor(role string) *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: mediaRequestTestID, Role: role}
}

// Exercise the real middleware with fixture authentication outside the JSON gate.
func mediaRequestFixtureHTTP(actor *ports.Actor, next http.Handler) http.Handler {
	strict := StrictMediaRequestJSON(next)
	return MediaRequestsNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, actor))
		}
		strict.ServeHTTP(w, r)
	}))
}

func TestStrictMediaRequestJSON(t *testing.T) {
	for _, family := range []struct {
		name, path, body string
		fields           []string
	}{
		{"submit", mediaRequestsPrefix, `{"media_type":"tv","tmdb_id":1,"seasons":[1],"include_specials":false}`, []string{"media_type", "tmdb_id", "seasons", "include_specials"}},
		{"close", mediaRequestsPrefix + "/" + mediaRequestTestID + "/close", `{"expected_version":1,"action":"withdraw","reason":""}`, []string{"expected_version", "action", "reason"}},
		{"approve", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approve", `{"expected_version":1,"root_id":"` + mediaRequestTestID + `","quality_policy_id":"default","indexer_ids":["` + mediaRequestTestID + `"],"downloader_id":"` + mediaRequestTestID + `"}`, []string{"expected_version", "root_id", "quality_policy_id", "indexer_ids", "downloader_id"}},
	} {
		t.Run(family.name, func(t *testing.T) {
			tests := []struct {
				name, body string
				valid      bool
			}{
				{"valid", family.body, true},
				{"root_null", "null", false}, {"root_array", "[]", false},
				{"two_values", family.body + `{}`, false},
				{"malformed", family.body[:len(family.body)-1], false},
				{"unknown", strings.Replace(family.body, `{`, `{"owner_id":"`+mediaRequestTestID+`",`, 1), false},
				{"deep_duplicate", strings.Replace(family.body, `{`, `{"unknown":{"x":1,"x":2},`, 1), false},
				{"deep_escaped_duplicate", strings.Replace(family.body, `{`, `{"unknown":{"x":1,"\u0078":2},`, 1), false},
				{"too_large", family.body + strings.Repeat(" ", 32<<10), false},
			}
			for _, field := range family.fields {
				var object map[string]json.RawMessage
				if err := json.Unmarshal([]byte(family.body), &object); err != nil {
					t.Fatal(err)
				}
				delete(object, field)
				missing, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				tests = append(tests, struct {
					name, body string
					valid      bool
				}{"missing_" + field, string(missing), false})
				object[field] = json.RawMessage("null")
				null, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				quoted := `"` + field + `"`
				tests = append(tests,
					struct {
						name, body string
						valid      bool
					}{"null_" + field, string(null), false},
					struct {
						name, body string
						valid      bool
					}{"duplicate_" + field, strings.Replace(family.body, quoted+":", quoted+`:0,`+quoted+":", 1), false},
					struct {
						name, body string
						valid      bool
					}{"escaped_duplicate_" + field, strings.Replace(family.body, quoted+":", `"\u00`+string([]byte{'3', '0'})+`":0,`+quoted+":", 1), false},
					struct {
						name, body string
						valid      bool
					}{"case_alias_" + field, strings.Replace(family.body, quoted, `"`+strings.ToUpper(field)+`"`, 1), false},
				)
			}
			// Use the exact same decoded property name with an escaped first letter.
			tests = append(tests, struct {
				name, body string
				valid      bool
			}{"escaped_actual_duplicate", strings.Replace(family.body, `{`, `{"\u00`+map[string]string{"submit": "6dedia_type", "close": "65xpected_version", "approve": "65xpected_version"}[family.name]+`":1,`, 1), false})
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					called := false
					handler := mediaRequestFixtureHTTP(mediaRequestFixtureActor(ports.RoleAdmin), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						called = true
						raw, err := io.ReadAll(r.Body)
						if err != nil || !bytes.Equal(raw, []byte(test.body)) {
							t.Fatal("valid request bytes changed")
						}
						w.WriteHeader(http.StatusNoContent)
					}))
					request := httptest.NewRequest(http.MethodPost, family.path, strings.NewReader(test.body))
					request.Header.Set("Idempotency-Key", "request:fixture_1.-")
					out := httptest.NewRecorder()
					handler.ServeHTTP(out, request)
					want := http.StatusBadRequest
					if test.valid {
						want = http.StatusNoContent
					}
					if out.Code != want || called != test.valid || out.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatalf("status=%d called=%v cache=%q", out.Code, called, out.Header().Get("Cache-Control"))
					}
				})
			}
		})
	}
}

func TestMediaRequestNumericAndShapeBoundaries(t *testing.T) {
	const submit = `{"media_type":"movie","tmdb_id":1,"seasons":[],"include_specials":false}`
	const closeBody = `{"expected_version":1,"action":"withdraw","reason":""}`
	const approve = `{"expected_version":1,"root_id":"` + mediaRequestTestID + `","quality_policy_id":"default","indexer_ids":["` + mediaRequestTestID + `"],"downloader_id":"` + mediaRequestTestID + `"}`
	for _, test := range []struct {
		name, path, body string
		valid            bool
	}{
		{"safe_id_max", mediaRequestsPrefix, strings.Replace(submit, `"tmdb_id":1`, `"tmdb_id":9007199254740991`, 1), true},
		{"unsafe_id", mediaRequestsPrefix, strings.Replace(submit, `"tmdb_id":1`, `"tmdb_id":9007199254740992`, 1), false},
		{"zero_id", mediaRequestsPrefix, strings.Replace(submit, `"tmdb_id":1`, `"tmdb_id":0`, 1), false},
		{"negative_id", mediaRequestsPrefix, strings.Replace(submit, `"tmdb_id":1`, `"tmdb_id":-1`, 1), false},
		{"fraction_id", mediaRequestsPrefix, strings.Replace(submit, `"tmdb_id":1`, `"tmdb_id":1.5`, 1), false},
		{"exponent_id", mediaRequestsPrefix, strings.Replace(submit, `"tmdb_id":1`, `"tmdb_id":1e0`, 1), false},
		{"string_id", mediaRequestsPrefix, strings.Replace(submit, `"tmdb_id":1`, `"tmdb_id":"1"`, 1), false},
		{"wrong_type", mediaRequestsPrefix, strings.Replace(submit, `"movie"`, `"all"`, 1), false},
		{"null_season", mediaRequestsPrefix, strings.Replace(submit, `[]`, `[null]`, 1), false},
		{"fraction_season", mediaRequestsPrefix, strings.Replace(submit, `[]`, `[1.5]`, 1), false},
		{"season_object_duplicate", mediaRequestsPrefix, strings.Replace(submit, `[]`, `[{"x":1,"x":2}]`, 1), false},
		{"season_object_escaped_duplicate", mediaRequestsPrefix, strings.Replace(submit, `[]`, `[{"x":1,"\u0078":2}]`, 1), false},
		{"too_many_seasons", mediaRequestsPrefix, strings.Replace(submit, `[]`, `[`+strings.Repeat("1,", 64)+`1]`, 1), false},
		{"wrong_boolean", mediaRequestsPrefix, strings.Replace(submit, `false`, `"false"`, 1), false},
		{"zero_version", mediaRequestsPrefix + "/" + mediaRequestTestID + "/close", strings.Replace(closeBody, `:1`, `:0`, 1), false},
		{"fraction_version", mediaRequestsPrefix + "/" + mediaRequestTestID + "/close", strings.Replace(closeBody, `:1`, `:1.1`, 1), false},
		{"overflow_version", mediaRequestsPrefix + "/" + mediaRequestTestID + "/close", strings.Replace(closeBody, `:1`, `:9223372036854775808`, 1), false},
		{"version_int64_max", mediaRequestsPrefix + "/" + mediaRequestTestID + "/close", strings.Replace(closeBody, `:1`, `:9223372036854775807`, 1), true},
		{"bad_action", mediaRequestsPrefix + "/" + mediaRequestTestID + "/close", strings.Replace(closeBody, `withdraw`, `approve`, 1), false},
		{"unicode_reason_max", mediaRequestsPrefix + "/" + mediaRequestTestID + "/close", strings.Replace(closeBody, `"reason":""`, `"reason":"`+strings.Repeat("中", 512)+`"`, 1), true},
		{"reason_too_long", mediaRequestsPrefix + "/" + mediaRequestTestID + "/close", strings.Replace(closeBody, `"reason":""`, `"reason":"`+strings.Repeat("中", 513)+`"`, 1), false},
		{"approve_valid", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approve", approve, true},
		{"approve_empty_indexers", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approve", strings.Replace(approve, `["`+mediaRequestTestID+`"]`, `[]`, 1), false},
		{"approve_too_many_indexers", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approve", strings.Replace(approve, `["`+mediaRequestTestID+`"]`, `[`+strings.Repeat(`"`+mediaRequestTestID+`",`, 64)+`"`+mediaRequestTestID+`"]`, 1), false},
		{"approve_invalid_root", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approve", strings.Replace(approve, `"root_id":"`+mediaRequestTestID+`"`, `"root_id":"invalid"`, 1), false},
		{"approve_zero_downloader", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approve", strings.Replace(approve, `"downloader_id":"`+mediaRequestTestID+`"`, `"downloader_id":"00000000-0000-0000-0000-000000000000"`, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := mediaRequestFixtureHTTP(mediaRequestFixtureActor(ports.RoleAdmin), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Idempotency-Key", "fixture")
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, request)
			want := http.StatusBadRequest
			if test.valid {
				want = http.StatusNoContent
			}
			if out.Code != want {
				t.Fatalf("status=%d want=%d", out.Code, want)
			}
		})
	}
}

func TestMediaRequestAuthenticationBeforeJSON(t *testing.T) {
	for _, path := range []string{mediaRequestsPrefix, mediaRequestsPrefix + "/commands/invalid!", mediaRequestsPrefix + "/" + mediaRequestTestID + "/close", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approve", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approval-commands/invalid!"} {
		for _, test := range []struct {
			name  string
			actor *ports.Actor
			want  int
		}{
			{"anonymous", nil, http.StatusUnauthorized},
			{"empty_scope_token", &ports.Actor{Type: ports.ActorTypeUser, ID: mediaRequestTestID, Role: ports.RoleAdmin, Scopes: []string{}}, http.StatusForbidden},
			{"read_token", &ports.Actor{Type: ports.ActorTypeUser, ID: mediaRequestTestID, Role: ports.RoleAdmin, Scopes: []string{"read"}}, http.StatusForbidden},
			{"service", &ports.Actor{Type: ports.ActorTypeService, ID: mediaRequestTestID, Role: ports.RoleAdmin}, http.StatusForbidden},
			{"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: mediaRequestTestID, Role: ports.RoleAdmin}, http.StatusForbidden},
			{"invalid_id", &ports.Actor{Type: ports.ActorTypeUser, ID: "invalid", Role: ports.RoleAdmin}, http.StatusForbidden},
		} {
			t.Run(path+"/"+test.name, func(t *testing.T) {
				called := false
				handler := mediaRequestFixtureHTTP(test.actor, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"malformed":`)))
				if out.Code != test.want || called || !strings.Contains(out.Header().Get("Cache-Control"), "no-store") {
					t.Fatalf("status=%d called=%v", out.Code, called)
				}
			})
		}
	}
	for _, path := range []string{mediaRequestsPrefix + "/" + mediaRequestTestID + "/approve", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approval-commands/key"} {
		out := httptest.NewRecorder()
		mediaRequestFixtureHTTP(mediaRequestFixtureActor(ports.RoleMember), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("member admitted to approval") })).ServeHTTP(out, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`null`)))
		if out.Code != http.StatusForbidden {
			t.Fatal("member approval must fail before JSON", out.Code)
		}
	}
}

func TestMediaRequestKeysAndPagination(t *testing.T) {
	const body = `{"media_type":"movie","tmdb_id":1,"seasons":[],"include_specials":false}`
	for _, keys := range [][]string{nil, {""}, {"one", "two"}, {"has space"}, {"comma,key"}, {"非ASCII"}, {strings.Repeat("k", 81)}, {"A:a_1.b-c"}, {strings.Repeat("k", 80)}} {
		request := httptest.NewRequest(http.MethodPost, mediaRequestsPrefix, strings.NewReader(body))
		for _, key := range keys {
			request.Header.Add("Idempotency-Key", key)
		}
		out := httptest.NewRecorder()
		mediaRequestFixtureHTTP(mediaRequestFixtureActor(ports.RoleMember), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(out, request)
		want := http.StatusBadRequest
		if len(keys) == 1 && validMediaRequestKey(keys[0]) {
			want = http.StatusNoContent
		}
		if out.Code != want {
			t.Fatal("invalid header boundary", out.Code, want)
		}
	}
	for _, test := range []struct {
		query string
		valid bool
	}{
		{"", true}, {"?limit=1", true}, {"?limit=100&before_id=" + mediaRequestTestID, true},
		{"?limit=0", false}, {"?limit=101", false}, {"?limit=-1", false}, {"?limit=01", false}, {"?limit=1&limit=2", false},
		{"?before_id=bad", false}, {"?before_id=", false}, {"?before_id=00000000-0000-0000-0000-000000000000", false},
		{"?before_id=" + mediaRequestTestID + "&before_id=" + mediaRequestTestID, false}, {"?limit=%xx", false}, {"?owner_id=" + mediaRequestTestID, false},
	} {
		out := httptest.NewRecorder()
		mediaRequestFixtureHTTP(mediaRequestFixtureActor(ports.RoleMember), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(out, httptest.NewRequest(http.MethodGet, mediaRequestsPrefix+test.query, nil))
		want := http.StatusBadRequest
		if test.valid {
			want = http.StatusNoContent
		}
		if out.Code != want {
			t.Fatalf("query %q: status=%d want=%d", test.query, out.Code, want)
		}
	}
}

func TestMediaRequestsNoStoreIncludesEveryOutcome(t *testing.T) {
	for _, path := range []string{mediaRequestsPrefix, mediaRequestsPrefix + "/", mediaRequestsPrefix + "/commands", mediaRequestsPrefix + "/commands/key", mediaRequestsPrefix + "/" + mediaRequestTestID, mediaRequestsPrefix + "/" + mediaRequestTestID + "/close", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approve", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approval-commands", mediaRequestsPrefix + "/" + mediaRequestTestID + "/approval-commands/key"} {
		for _, status := range []int{200, 201, 400, 401, 403, 404, 409, 422, 429, 503} {
			out := httptest.NewRecorder()
			MediaRequestsNoStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })).ServeHTTP(out, httptest.NewRequest(http.MethodGet, path, nil))
			if out.Code != status || out.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("%s status=%d cache=%q", path, out.Code, out.Header().Get("Cache-Control"))
			}
		}
	}
	out := httptest.NewRecorder()
	MediaRequestsNoStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(out, httptest.NewRequest(http.MethodGet, mediaRequestsPrefix+"-other", nil))
	if out.Header().Get("Cache-Control") != "" {
		t.Fatal("unrelated route matched media request prefix")
	}
}

func TestMediaRequestSafeErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{ports.ErrUnauthorized, 401}, {ports.ErrForbidden, 403}, {ports.ErrNotFound, 404},
		{ports.ErrMediaRequestConflict, 409}, {ports.ErrSubscriptionBindingsConflict, 409},
		{ports.ErrRejected, 422}, {ports.ErrInvalidSubscriptionBindings, 422}, {ports.ErrNoSubscriptionRoot, 422}, {ports.ErrAmbiguousSubscriptionRoot, 422},
		{ports.ErrMediaRequestLimit, 429}, {ports.ErrUnavailable, 503}, {errors.New("private provider credential"), 503},
	} {
		ctx := context.WithValue(context.Background(), requestIDKey, "fixture-request")
		out := httptest.NewRecorder()
		if err := mediaRequestError(ctx, test.err).VisitSubmitMediaRequestResponse(out); err != nil {
			t.Fatal(err)
		}
		var body ErrorResponse
		if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if out.Code != test.status || body.Status != test.status || body.RequestId != "fixture-request" || strings.Contains(out.Body.String(), "private provider credential") || out.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatal("unsafe error response", out.Code, out.Body.String())
		}
	}
}

func TestMediaRequestApprovalPublicSubscriptionFormat(t *testing.T) {
	id := uuid.MustParse(mediaRequestTestID)
	receipt := ports.MediaRequestApprovalReceipt{RequestID: id, Subscription: ports.SubscriptionCreationReceipt{
		Subscription: ports.SubscriptionRecord{ID: id, UserID: id, MediaID: id, RootID: id, QualityPolicyID: "default", QualityPolicyRevision: 2, IndexerIDs: []uuid.UUID{id}, DownloaderID: &id, State: "active", IntentVersion: 3, ScopeType: "all"},
		Wanted:       []ports.SubscriptionCreationWanted{{ID: id, UnitID: id, UnitKey: "movie", IsSkipped: false, IsSatisfied: true}},
	}}
	raw, err := json.Marshal(mediaRequestApprovalResult(receipt))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var sub map[string]json.RawMessage
	if err := json.Unmarshal(body["subscription"], &sub); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "user_id", "media_id", "root_id", "quality_policy_id", "quality_policy_revision", "indexer_ids", "downloader_id", "state", "intent_version", "scope_type", "wanted_items"} {
		if sub[key] == nil {
			t.Fatal("missing public subscription field", key)
		}
	}
	if sub["ID"] != nil || sub["UserID"] != nil || body["request_id"] == nil {
		t.Fatal("ports record leaked into public JSON", string(raw))
	}
	var wanted []map[string]json.RawMessage
	if err := json.Unmarshal(sub["wanted_items"], &wanted); err != nil || len(wanted) != 1 || string(wanted[0]["is_satisfied"]) != "true" || string(wanted[0]["unit_key"]) != `"movie"` {
		t.Fatal("original Wanted snapshot missing", string(raw))
	}
}
