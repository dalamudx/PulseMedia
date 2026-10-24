package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

const untrackedReviewFileID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const untrackedReviewID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
const untrackedReviewPath = untrackedScopePath + "/discoveries/" + untrackedReviewFileID
const untrackedReviewBody = `{"scope_version":2,"file_revision":3,"media_type":"movie","tmdb_id":0}`

var untrackedReviewHTTPRoutes = []struct{ method, suffix, body, key string }{
	{"POST", "/reviews", untrackedReviewBody, "review-key"},
	{"GET", "/reviews/" + untrackedReviewID, "", ""},
	{"GET", "/review-commands/review-key", "", ""},
	{"POST", "/confirmations", "", "confirmation-key"},
	{"GET", "/confirmation-commands/confirmation-key", "", ""},
}

type untrackedReviewHTTPFake struct {
	request                                                               *ports.UntrackedReviewRequestReceipt
	review                                                                *ports.UntrackedReview
	confirmation                                                          *ports.UntrackedConfirmationReceipt
	err                                                                   error
	requests, reads, requestReceipts, confirmations, confirmationReceipts int
	actor                                                                 ports.Actor
	scopeID, fileID, reviewID                                             uuid.UUID
	input                                                                 ports.UntrackedReviewInput
	confirmationInput                                                     ports.UntrackedConfirmationInput
	key                                                                   string
}

func (f *untrackedReviewHTTPFake) track(a *ports.Actor, scopeID, fileID uuid.UUID, key string) {
	f.actor, f.scopeID, f.fileID, f.key = *a, scopeID, fileID, key
}
func (f *untrackedReviewHTTPFake) calls() int {
	return f.requests + f.reads + f.requestReceipts + f.confirmations + f.confirmationReceipts
}
func (f *untrackedReviewHTTPFake) Request(_ context.Context, a *ports.Actor, scopeID, fileID uuid.UUID, input ports.UntrackedReviewInput, key string) (*ports.UntrackedReviewRequestReceipt, error) {
	f.track(a, scopeID, fileID, key)
	f.requests++
	f.input = input
	return f.request, f.err
}
func (f *untrackedReviewHTTPFake) RequestReceipt(_ context.Context, a *ports.Actor, scopeID, fileID uuid.UUID, key string) (*ports.UntrackedReviewRequestReceipt, error) {
	f.track(a, scopeID, fileID, key)
	f.requestReceipts++
	return f.request, f.err
}
func (f *untrackedReviewHTTPFake) Read(_ context.Context, a *ports.Actor, scopeID, fileID, reviewID uuid.UUID) (*ports.UntrackedReview, error) {
	f.track(a, scopeID, fileID, "")
	f.reads++
	f.reviewID = reviewID
	return f.review, f.err
}
func (f *untrackedReviewHTTPFake) Confirm(_ context.Context, a *ports.Actor, scopeID, fileID uuid.UUID, input ports.UntrackedConfirmationInput, key string) (*ports.UntrackedConfirmationReceipt, error) {
	f.track(a, scopeID, fileID, key)
	f.confirmations++
	f.confirmationInput = input
	return f.confirmation, f.err
}
func (f *untrackedReviewHTTPFake) ConfirmationReceipt(_ context.Context, a *ports.Actor, scopeID, fileID uuid.UUID, key string) (*ports.UntrackedConfirmationReceipt, error) {
	f.track(a, scopeID, fileID, key)
	f.confirmationReceipts++
	return f.confirmation, f.err
}

// Exercise the actual outer no-store/auth/strict guard and generated router order.
type untrackedReviewRouterApp struct{ AppContext }

func (untrackedReviewRouterApp) Clock() ports.Clock {
	return checkinConfigClock{time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
}
func (untrackedReviewRouterApp) IDGen() ports.IDGen { return untrackedReviewHTTPIDs{} }
func (untrackedReviewRouterApp) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type untrackedReviewHTTPIDs struct{}

func (untrackedReviewHTTPIDs) NewUUID() uuid.UUID {
	return uuid.MustParse("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
}
func (g untrackedReviewHTTPIDs) NewUUIDString() string { return g.NewUUID().String() }
func untrackedReviewHTTPRouter(s *ServerHandler, actor *ports.Actor) http.Handler {
	h := NewRouter(untrackedReviewRouterApp{}, s, nil, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, actor))
		}
		h.ServeHTTP(w, r)
	})
}

func untrackedReviewHTTPFixture() (*ServerHandler, *untrackedReviewHTTPFake) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scopeID, fileID := uuid.MustParse(untrackedScopeID), uuid.MustParse(untrackedReviewFileID)
	actorID := uuid.MustParse(untrackedScopeAdmin().ID)
	request := ports.UntrackedReviewRequest{ID: uuid.MustParse(untrackedReviewID), ScopeID: scopeID, FileID: fileID, ActorID: actorID, Input: ports.UntrackedReviewInput{ScopeVersion: 2, FileRevision: 3, MediaType: "movie"}, JobID: uuid.MustParse("dddddddd-dddd-4ddd-8ddd-dddddddddddd"), CreatedAt: now}
	review := ports.UntrackedReview{Request: request, State: "ready", Evidence: &ports.UntrackedReviewEvidence{ParserVersion: "parser.v1", ParsedTitle: "Movie", ParsedYear: 2025, Selection: "automatic", Complete: true, Reason: "ready", Candidate: &ports.UntrackedReviewCandidate{Key: "tmdb.movie:550", Namespace: "tmdb.movie", TMDBID: 550, Title: "Movie", OriginalTitle: "Original", Year: 2025, Units: []ports.UntrackedReviewUnit{{UnitKey: "movie"}}}}, MaterialHash: strings.Repeat("a", 64), RecordedAt: &now}
	input := ports.UntrackedConfirmationInput{ReviewID: request.ID, MaterialHash: review.MaterialHash, ScopeVersion: 2, FileRevision: 3, CandidateKey: "tmdb.movie:550", Action: "hardlink", TargetRelativePath: "Movie (2025)/Movie.mkv"}
	scope := ports.UntrackedScope{ID: scopeID, OwnerID: actorID, Enabled: true, Version: 2, ClientID: uuid.MustParse("33333333-3333-4333-8333-333333333333"), ClientVersion: now, ClientStamp: strings.Repeat("b", 64), RemotePrefix: "/downloads", LocalRoot: "/configured/downloads", SourceIdentity: ports.FileIdentity{Device: 11, Inode: 12, Size: 999, Mtime: 888}, TargetRootID: uuid.MustParse("44444444-4444-4444-8444-444444444444"), TargetRootPath: "/configured/movies", TargetRootGeneration: 1, TargetMediaType: "movie", TargetMarker: "marker", TargetBaselineScanID: uuid.MustParse("55555555-5555-4555-8555-555555555555"), TargetIdentity: ports.FileIdentity{Device: 21, Inode: 22, Size: 777, Mtime: 666}, CreatedAt: now, UpdatedAt: now}
	file := ports.UntrackedDiscoveredFile{ID: fileID, ScopeID: scopeID, ScopeVersion: 2, Revision: 3, RelativePath: "Movie.mkv", Identity: ports.FileIdentity{Device: 11, Inode: 13, Size: 12345, Mtime: 123}, FirstObservedAt: now, LastObservedAt: now, Observations: 2}
	f := &untrackedReviewHTTPFake{request: &ports.UntrackedReviewRequestReceipt{CommandKey: "review-key", Request: request}, review: &review, confirmation: &ports.UntrackedConfirmationReceipt{CommandKey: "confirmation-key", Material: ports.UntrackedConfirmationMaterial{ID: uuid.MustParse("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"), ActorID: actorID, Scope: scope, File: file, Review: review, Input: input, CreatedAt: now}, State: "confirmed_pending_organization"}}
	s := &ServerHandler{}
	s.SetUntrackedReviewService(f)
	return s, f
}

func untrackedReviewHTTPJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func untrackedReviewHTTPStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d want=%d cache=%q body=%s", w.Code, status, w.Header().Get("Cache-Control"), w.Body.String())
	}
	if status >= 400 && !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatal("expected safe problem response", w.Header())
	}
}

func TestUntrackedReviewHTTPRequestReceipt(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		for _, tmdbID := range []int64{0, 550} {
			t.Run(fmt.Sprintf("%s-%d", kind, tmdbID), func(t *testing.T) {
				s, f := untrackedReviewHTTPFixture()
				f.request.Request.Input.MediaType, f.request.Request.Input.TMDBID = kind, tmdbID
				body := untrackedReviewHTTPJSON(t, f.request.Request.Input)
				h := untrackedReviewHTTPRouter(s, untrackedScopeAdmin())
				w := untrackedScopeCall(h, "POST", untrackedReviewPath+"/reviews", body, "review-key")
				untrackedReviewHTTPStatus(t, w, 202)
				var out ports.UntrackedReviewRequestReceipt
				if json.Unmarshal(w.Body.Bytes(), &out) != nil || !reflect.DeepEqual(out, *f.request) || f.input != f.request.Request.Input || f.requests != 1 || f.scopeID != f.request.Request.ScopeID || f.fileID != f.request.Request.FileID || f.key != "review-key" || !reflect.DeepEqual(f.actor, *untrackedScopeAdmin()) {
					t.Fatal("request/job/input/actor mapping mismatch", w.Body.String(), f)
				}
				original := w.Body.String()
				f.review.Request.Input.ScopeVersion = 9
				w = untrackedScopeCall(h, "GET", untrackedReviewPath+"/review-commands/review-key", "")
				untrackedReviewHTTPStatus(t, w, 200)
				if w.Body.String() != original || f.requests != 1 || f.confirmations != 0 || f.requestReceipts != 1 {
					t.Fatal("GET did not recover the original request/job", w.Body.String(), f)
				}
			})
		}
	}
}

func TestUntrackedReviewHTTPReadOnlyViews(t *testing.T) {
	for _, state := range []string{"queued", "running", "ready", "review_required", "stale", "tv-air-date"} {
		t.Run(state, func(t *testing.T) {
			s, f := untrackedReviewHTTPFixture()
			switch state {
			case "queued", "running":
				f.review.State, f.review.Evidence, f.review.RecordedAt, f.review.MaterialHash = state, nil, nil, ""
			case "review_required":
				f.review.State, f.review.Evidence.Complete, f.review.Evidence.Candidate, f.review.Evidence.Reason = state, false, nil, "no_match"
			case "stale":
				f.review.Stale = true
			case "tv-air-date":
				date := time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)
				f.review.Request.Input.MediaType = "tv"
				c := f.review.Evidence.Candidate
				c.Key, c.Namespace = "tmdb.tv:550", "tmdb.tv"
				c.Units = []ports.UntrackedReviewUnit{{UnitKey: "S01E01", TMDBEpisodeID: 123, Season: 1, Episode: 1, AirDate: &date}}
			}
			w := untrackedScopeCall(untrackedReviewHTTPRouter(s, untrackedScopeAdmin()), "GET", untrackedReviewPath+"/reviews/"+untrackedReviewID, "")
			untrackedReviewHTTPStatus(t, w, 200)
			var out ports.UntrackedReview
			if json.Unmarshal(w.Body.Bytes(), &out) != nil || !reflect.DeepEqual(out, *f.review) || f.reads != 1 || f.reviewID != uuid.MustParse(untrackedReviewID) || f.requests != 0 || f.confirmations != 0 {
				t.Fatal("view mapping or read-only contract", w.Body.String(), f)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(w.Body.Bytes(), &fields) != nil || len(fields) != 7 {
				t.Fatal("view must expose exactly seven lowercase fields", w.Body.String())
			}
			for _, key := range []string{"request", "state", "evidence", "material_hash", "recorded_at", "stale", "execution_allowed"} {
				if _, ok := fields[key]; !ok {
					t.Fatal("missing required field", key)
				}
			}
			if state == "ready" && !strings.Contains(w.Body.String(), `"air_date":null`) {
				t.Fatal("unknown air date must remain null")
			}
		})
	}
}

func TestUntrackedConfirmationHTTPFrozenReceipt(t *testing.T) {
	for _, action := range []string{"hardlink", "copy", "reflink", "move"} {
		t.Run(action, func(t *testing.T) {
			s, f := untrackedReviewHTTPFixture()
			f.confirmation.Material.Input.Action = action
			h := untrackedReviewHTTPRouter(s, untrackedScopeAdmin())
			w := untrackedScopeCall(h, "POST", untrackedReviewPath+"/confirmations", untrackedReviewHTTPJSON(t, f.confirmation.Material.Input), "confirmation-key")
			untrackedReviewHTTPStatus(t, w, 200)
			original := w.Body.String()
			if f.confirmations != 1 || f.confirmationInput != f.confirmation.Material.Input || !strings.Contains(original, `"state":"confirmed_pending_organization"`) || strings.Contains(original, `"execution_allowed":true`) {
				t.Fatal("confirmation must only freeze intent", original, f)
			}
			var out UntrackedConfirmationReceipt
			if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Material.File.Size != 12345 || out.Material.File.Mtime != 123 || out.Material.File.RelativePath != "Movie.mkv" || out.Material.Scope.SourceIdentity.Inode != 12 || out.Material.Scope.TargetIdentity.Inode != 22 {
				t.Fatal("explicit nested material mapping", original)
			}
			var wire struct {
				Material struct {
					Scope struct {
						Source map[string]json.RawMessage `json:"source_identity"`
						Target map[string]json.RawMessage `json:"target_identity"`
					}
					File map[string]json.RawMessage
				}
			}
			if json.Unmarshal(w.Body.Bytes(), &wire) != nil || len(wire.Material.Scope.Source) != 2 || len(wire.Material.Scope.Target) != 2 || len(wire.Material.File) != 12 {
				t.Fatal("scope identity must exclude size/mtime; file must retain observation fields", original)
			}
			f.review.Stale, f.review.Request.Input.ScopeVersion = true, 99
			w = untrackedScopeCall(h, "GET", untrackedReviewPath+"/confirmation-commands/confirmation-key", "")
			untrackedReviewHTTPStatus(t, w, 200)
			if w.Body.String() != original || f.confirmations != 1 || f.requests != 0 || f.confirmationReceipts != 1 {
				t.Fatal("historical GET must preserve frozen scope/file/review/timestamps", w.Body.String(), f)
			}
		})
	}
}

func TestUntrackedReviewHTTPSubjectsBeforeTransport(t *testing.T) {
	for _, subject := range []string{"anonymous", "member", "token", "system", "service", "empty-scopes", "scopes", "invalid-id", "nil-id", "compact-id", "uppercase-id"} {
		t.Run(subject, func(t *testing.T) {
			actor, status := untrackedScopeAdmin(), 403
			switch subject {
			case "anonymous":
				actor, status = nil, 401
			case "member":
				actor.Role = ports.RoleMember
			case "token":
				actor.Type = "api_token"
			case "system":
				actor.Type = ports.ActorTypeSystem
			case "service":
				actor.Type = ports.ActorTypeService
			case "empty-scopes":
				actor.Scopes = []string{}
			case "scopes":
				actor.Scopes = []string{"admin", "write", "read"}
			case "invalid-id":
				actor.ID = "bad"
			case "nil-id":
				actor.ID = uuid.Nil.String()
			case "compact-id":
				actor.ID = "22222222222242228222222222222222"
			case "uppercase-id":
				actor.ID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
			}
			s, f := untrackedReviewHTTPFixture()
			h := untrackedReviewHTTPRouter(s, actor)
			for _, route := range untrackedReviewHTTPRoutes {
				// Missing keys, invalid JSON and invalid UUID must not precede authentication.
				for _, path := range []string{untrackedReviewPath + route.suffix, strings.Replace(untrackedReviewPath, untrackedReviewFileID, "bad", 1) + route.suffix} {
					untrackedReviewHTTPStatus(t, untrackedScopeCall(h, route.method, path, "junk"), status)
				}
			}
			if f.calls() != 0 {
				t.Fatal("forbidden application call", f)
			}
		})
	}
}

func TestUntrackedReviewHTTPStrictJSONZeroCalls(t *testing.T) {
	_, fixture := untrackedReviewHTTPFixture()
	for _, endpoint := range []struct{ suffix, body, key string }{{"/reviews", untrackedReviewBody, "review-key"}, {"/confirmations", untrackedReviewHTTPJSON(t, fixture.confirmation.Material.Input), "confirmation-key"}} {
		t.Run(endpoint.suffix, func(t *testing.T) {
			bodies := map[string]string{"empty": "", "null": "null", "array": "[]", "object": "{}", "second-object": endpoint.body + "{}", "second-null": endpoint.body + "null", "junk": endpoint.body + "secret", "oversized": endpoint.body + strings.Repeat(" ", 4096), "unknown": strings.TrimSuffix(endpoint.body, "}") + `,"execution_allowed":true}`, "escaped-duplicate": strings.TrimSuffix(endpoint.body, "}") + `,"scope_vers\u0069on":2}`}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(endpoint.body), &fields); err != nil {
				t.Fatal(err)
			}
			for name, value := range fields {
				missing := map[string]json.RawMessage{}
				for k, v := range fields {
					if k != name {
						missing[k] = v
					}
				}
				bodies["missing-"+name] = untrackedReviewHTTPJSON(t, missing)
				bodies["duplicate-"+name] = strings.TrimSuffix(endpoint.body, "}") + fmt.Sprintf(`,%q:%s}`, name, value)
				bodies["case-"+name] = strings.Replace(endpoint.body, fmt.Sprintf(`%q:`, name), fmt.Sprintf(`%q:`, strings.ToUpper(name)), 1)
				for _, invalid := range []string{"null", "[]", "{}", "true"} {
					bodies[name+"-"+invalid] = strings.Replace(endpoint.body, fmt.Sprintf(`%q:%s`, name, value), fmt.Sprintf(`%q:%s`, name, invalid), 1)
				}
			}
			for name, body := range bodies {
				t.Run(name, func(t *testing.T) {
					s, f := untrackedReviewHTTPFixture()
					w := untrackedScopeCall(untrackedReviewHTTPRouter(s, untrackedScopeAdmin()), "POST", untrackedReviewPath+endpoint.suffix, body, endpoint.key)
					untrackedReviewHTTPStatus(t, w, 400)
					if f.calls() != 0 || strings.Contains(w.Body.String(), "secret") {
						t.Fatal("invalid JSON reached service or leaked content", f, w.Body.String())
					}
				})
			}
		})
	}
}

func TestUntrackedReviewHTTPInputValuesZeroCalls(t *testing.T) {
	_, fixture := untrackedReviewHTTPFixture()
	confirmation := untrackedReviewHTTPJSON(t, fixture.confirmation.Material.Input)
	for _, endpoint := range []struct{ suffix, body, key string }{{"/reviews", untrackedReviewBody, "review-key"}, {"/confirmations", confirmation, "confirmation-key"}} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(endpoint.body), &fields); err != nil {
			t.Fatal(err)
		}
		invalid := map[string][]string{"scope_version": {"0", "-1", "1.0", "1e0", "9223372036854775807", `"2"`}, "file_revision": {"0", "-1", "1.0", "1e0", "9223372036854775808"}}
		if endpoint.suffix == "/reviews" {
			invalid["tmdb_id"] = []string{"-1", "0.0", "0e0", `"0"`, "9223372036854775808"}
			invalid["media_type"] = []string{`"Movie"`, `""`, `"episode"`, "1"}
		} else {
			invalid["review_id"] = []string{`"00000000-0000-0000-0000-000000000000"`, `"bad"`, `"BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"`, `"bbbbbbbbbbbb4bbb8bbbbbbbbbbbbbbb"`}
			for _, hash := range []string{"", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 63)} {
				invalid["material_hash"] = append(invalid["material_hash"], untrackedReviewHTTPJSON(t, hash))
			}
			for _, key := range []string{"tmdb.movie:0", "tmdb.movie:-1", "tmdb.movie:+1", "tmdb.movie:01", "tmdb.movie:9223372036854775808", "tmdb.movie:1.0", "tmdb.movie:1e0", "tmdb.Movie:1", "tmdb.episode:1"} {
				invalid["candidate_key"] = append(invalid["candidate_key"], untrackedReviewHTTPJSON(t, key))
			}
			invalid["action"] = []string{`"HARDLINK"`, `"rename"`, `""`}
			for _, path := range []string{"", ".", "..", "../Movie.mkv", "/Movie.mkv", "//Movie.mkv", "a/../Movie.mkv", "a/./Movie.mkv", "a//Movie.mkv", "a\\Movie.mkv", "a\x00.mkv", "C:\\Movie.mkv", "Movie.nfo", "folder/", "Movie.mkv\n"} {
				invalid["target_relative_path"] = append(invalid["target_relative_path"], untrackedReviewHTTPJSON(t, path))
			}
		}
		for name, values := range invalid {
			for i, value := range values {
				t.Run(fmt.Sprintf("%s/%s/%d", endpoint.suffix, name, i), func(t *testing.T) {
					body := strings.Replace(endpoint.body, fmt.Sprintf(`%q:%s`, name, fields[name]), fmt.Sprintf(`%q:%s`, name, value), 1)
					s, f := untrackedReviewHTTPFixture()
					w := untrackedScopeCall(untrackedReviewHTTPRouter(s, untrackedScopeAdmin()), "POST", untrackedReviewPath+endpoint.suffix, body, endpoint.key)
					untrackedReviewHTTPStatus(t, w, 400)
					if f.calls() != 0 {
						t.Fatal("invalid value reached service", body, f)
					}
				})
			}
		}
	}
}

func TestUntrackedReviewHTTPTransportZeroCalls(t *testing.T) {
	_, fixture := untrackedReviewHTTPFixture()
	for i, route := range untrackedReviewHTTPRoutes {
		body := route.body
		if route.suffix == "/confirmations" {
			body = untrackedReviewHTTPJSON(t, fixture.confirmation.Material.Input)
		}
		var validKeys []string
		if route.key != "" {
			validKeys = []string{route.key}
		}
		tests := []struct {
			name, path, body string
			keys             []string
		}{
			{"query", untrackedReviewPath + route.suffix + "?secret=value", body, validKeys},
			{"bare-query", untrackedReviewPath + route.suffix + "?", body, validKeys},
			{"bad-scope", strings.Replace(untrackedReviewPath, untrackedScopeID, "bad", 1) + route.suffix, body, validKeys},
			{"nil-file", strings.Replace(untrackedReviewPath, untrackedReviewFileID, uuid.Nil.String(), 1) + route.suffix, body, validKeys},
			{"uppercase-file", strings.Replace(untrackedReviewPath, untrackedReviewFileID, strings.ToUpper(untrackedReviewFileID), 1) + route.suffix, body, validKeys},
		}
		if route.method == "GET" {
			tests = append(tests, struct {
				name, path, body string
				keys             []string
			}{"get-body", untrackedReviewPath + route.suffix, " ", nil}, struct {
				name, path, body string
				keys             []string
			}{"get-key", untrackedReviewPath + route.suffix, "", []string{"forbidden"}})
		} else {
			for n, keys := range [][]string{nil, {""}, {"one", "two"}, {"one,two"}, {"has space"}, {strings.Repeat("a", 161)}} {
				tests = append(tests, struct {
					name, path, body string
					keys             []string
				}{fmt.Sprintf("key-%d", n), untrackedReviewPath + route.suffix, body, keys})
			}
		}
		for _, test := range tests {
			t.Run(fmt.Sprintf("%d/%s", i, test.name), func(t *testing.T) {
				s, f := untrackedReviewHTTPFixture()
				w := untrackedScopeCall(untrackedReviewHTTPRouter(s, untrackedScopeAdmin()), route.method, test.path, test.body, test.keys...)
				untrackedReviewHTTPStatus(t, w, 400)
				if f.calls() != 0 || strings.Contains(w.Body.String(), "secret") {
					t.Fatal("invalid transport reached service or leaked query", f, w.Body.String())
				}
			})
		}
	}
}

func TestUntrackedReviewHTTPSafeErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{"rejected", ports.ErrRejected, 400}, {"unauthorized", ports.ErrUnauthorized, 401}, {"forbidden", ports.ErrForbidden, 403}, {"not-found", ports.ErrNotFound, 404}, {"stale", fmt.Errorf("secret: %w", ports.ErrUntrackedReviewStale), 409}, {"conflict", ports.ErrEndpointVersionConflict, 409}, {"unavailable", ports.ErrUnavailable, 503}, {"unknown", errors.New("secret DSN cookie /private/source"), 503}, {"cancelled", context.Canceled, 503}, {"deadline", context.DeadlineExceeded, 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, f := untrackedReviewHTTPFixture()
			f.err = test.err
			h := untrackedReviewHTTPRouter(s, untrackedScopeAdmin())
			for _, route := range untrackedReviewHTTPRoutes {
				body := route.body
				if route.suffix == "/confirmations" {
					body = untrackedReviewHTTPJSON(t, f.confirmation.Material.Input)
				}
				var keys []string
				if route.key != "" {
					keys = []string{route.key}
				}
				w := untrackedScopeCall(h, route.method, untrackedReviewPath+route.suffix, body, keys...)
				untrackedReviewHTTPStatus(t, w, test.status)
				for _, secret := range []string{"secret", "DSN", "cookie", "/private"} {
					if strings.Contains(w.Body.String(), secret) {
						t.Fatal("unsafe error", w.Body.String())
					}
				}
			}
			if f.calls() != 5 || f.requests != 1 || f.confirmations != 1 {
				t.Fatal("unexpected dispatch", f)
			}
		})
	}
}

func TestUntrackedReviewHTTPRejectsMalformedResults(t *testing.T) {
	for _, mutation := range []string{"request-nil", "request-allowed", "request-key", "request-job", "request-owner", "request-time", "review-nil", "review-allowed", "review-id", "review-state", "review-evidence", "confirmation-nil", "confirmation-allowed", "confirmation-review-allowed", "confirmation-state", "confirmation-id", "confirmation-file", "confirmation-hash", "confirmation-root-type", "confirmation-file-identity", "confirmation-time"} {
		t.Run(mutation, func(t *testing.T) {
			s, f := untrackedReviewHTTPFixture()
			body := untrackedReviewHTTPJSON(t, f.confirmation.Material.Input)
			switch mutation {
			case "request-nil":
				f.request = nil
			case "request-allowed":
				f.request.ExecutionAllowed = true
			case "request-key":
				f.request.CommandKey = "other"
			case "request-job":
				f.request.Request.JobID = uuid.Nil
			case "request-owner":
				f.request.Request.ActorID = uuid.Nil
			case "request-time":
				f.request.Request.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			case "review-nil":
				f.review = nil
			case "review-allowed":
				f.review.ExecutionAllowed = true
			case "review-id":
				f.review.Request.ID = uuid.Nil
			case "review-state":
				f.review.State = "imported"
			case "review-evidence":
				f.review.Evidence = nil
			case "confirmation-nil":
				f.confirmation = nil
			case "confirmation-allowed":
				f.confirmation.ExecutionAllowed = true
			case "confirmation-review-allowed":
				f.confirmation.Material.Review.ExecutionAllowed = true
			case "confirmation-state":
				f.confirmation.State = "organized"
			case "confirmation-id":
				f.confirmation.Material.ID = uuid.Nil
			case "confirmation-file":
				f.confirmation.Material.File.ID = uuid.Nil
			case "confirmation-hash":
				f.confirmation.Material.Review.MaterialHash = strings.Repeat("b", 64)
			case "confirmation-root-type":
				f.confirmation.Material.Scope.TargetMediaType = "tv"
			case "confirmation-file-identity":
				f.confirmation.Material.File.Identity.Inode = 0
			case "confirmation-time":
				f.confirmation.Material.CreatedAt = time.Time{}
			}
			h := untrackedReviewHTTPRouter(s, untrackedScopeAdmin())
			routes := untrackedReviewHTTPRoutes[:1]
			if strings.HasPrefix(mutation, "request-") {
				routes = []struct{ method, suffix, body, key string }{untrackedReviewHTTPRoutes[0], untrackedReviewHTTPRoutes[2]}
			}
			if strings.HasPrefix(mutation, "review-") {
				routes = untrackedReviewHTTPRoutes[1:2]
			}
			if strings.HasPrefix(mutation, "confirmation-") {
				routes = untrackedReviewHTTPRoutes[3:]
			}
			for _, route := range routes {
				input := route.body
				if route.suffix == "/confirmations" {
					input = body
				}
				var keys []string
				if route.key != "" {
					keys = []string{route.key}
				}
				untrackedReviewHTTPStatus(t, untrackedScopeCall(h, route.method, untrackedReviewPath+route.suffix, input, keys...), 503)
			}
		})
	}
}

func TestUntrackedReviewHTTPBodyBoundary(t *testing.T) {
	_, fixture := untrackedReviewHTTPFixture()
	for _, endpoint := range []struct{ suffix, body, key string }{{"/reviews", untrackedReviewBody, "review-key"}, {"/confirmations", untrackedReviewHTTPJSON(t, fixture.confirmation.Material.Input), "confirmation-key"}} {
		for _, size := range []int{4095, 4096} {
			t.Run(fmt.Sprintf("%s/%d", endpoint.suffix, size), func(t *testing.T) {
				s, f := untrackedReviewHTTPFixture()
				body := endpoint.body + strings.Repeat(" ", size-len(endpoint.body))
				w := untrackedScopeCall(untrackedReviewHTTPRouter(s, untrackedScopeAdmin()), "POST", untrackedReviewPath+endpoint.suffix, body, endpoint.key)
				status, calls := 400, 0
				if size == 4095 {
					status, calls = 200, 1
					if endpoint.suffix == "/reviews" {
						status = 202
					}
				}
				untrackedReviewHTTPStatus(t, w, status)
				if f.calls() != calls {
					t.Fatal("body boundary dispatch", f)
				}
			})
		}
	}
}

func TestUntrackedReviewHTTPMissingService(t *testing.T) {
	_, fixture := untrackedReviewHTTPFixture()
	h := untrackedReviewHTTPRouter(&ServerHandler{}, untrackedScopeAdmin())
	for _, route := range untrackedReviewHTTPRoutes {
		body := route.body
		if route.suffix == "/confirmations" {
			body = untrackedReviewHTTPJSON(t, fixture.confirmation.Material.Input)
		}
		var keys []string
		if route.key != "" {
			keys = []string{route.key}
		}
		untrackedReviewHTTPStatus(t, untrackedScopeCall(h, route.method, untrackedReviewPath+route.suffix, body, keys...), 503)
	}
}

func TestUntrackedReviewHTTPProductionCookieGuards(t *testing.T) {
	for _, test := range []struct {
		name                                                          string
		member, token, unavailable, missingOrigin, anonymous, invalid bool
		status                                                        int
	}{
		{name: "cookie-admin", status: 202}, {name: "cookie-member", member: true, status: 403}, {name: "admin-token", token: true, status: 403}, {name: "auth-unavailable", unavailable: true, status: 503}, {name: "csrf", missingOrigin: true, status: 403}, {name: "anonymous-invalid-before-binder", anonymous: true, invalid: true, status: 401}, {name: "admin-bad-uuid", invalid: true, status: 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, f := untrackedReviewHTTPFixture()
			clock := untrackedReviewRouterApp{}.Clock()
			secrets := crypto.AuthenticationSecrets{}
			credential := "review-session-fixture"
			user := ports.User{ID: uuid.MustParse(untrackedScopeAdmin().ID), Role: ports.RoleAdmin, IsActive: true}
			if test.member {
				user.Role = ports.RoleMember
			}
			repo := &checkinConfigAuthRepo{hash: secrets.HashToken(credential), user: user, expires: clock.Now().Add(time.Hour), token: test.token}
			if test.unavailable {
				repo.err = errors.New("secret cookie DSN")
			}
			h := NewRouter(untrackedReviewRouterApp{}, s, auth.NewService(repo, secrets, clock, nil), nil)
			path := untrackedReviewPath + "/reviews"
			if test.invalid {
				path = strings.Replace(path, untrackedReviewFileID, "bad", 1)
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(untrackedReviewBody))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "review-key")
			if !test.missingOrigin {
				r.Header.Set("Origin", "http://example.com")
			}
			if !test.anonymous {
				if test.token {
					r.Header.Set("Authorization", "Bearer "+credential)
				} else {
					r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: credential})
				}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			untrackedReviewHTTPStatus(t, w, test.status)
			wantCalls := 0
			if test.status == 202 {
				wantCalls = 1
			}
			if f.calls() != wantCalls || strings.Contains(w.Body.String(), "secret") {
				t.Fatal("outer guard dispatch or disclosure", f, w.Body.String())
			}
		})
	}
}
