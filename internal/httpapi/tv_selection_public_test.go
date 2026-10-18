package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

func selectionPublicRouter(f *completeTVFixture, configured bool) http.Handler {
	a := f.f.a
	authSvc := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	h := httpapi.NewServerHandler(a, authSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if configured {
		h.SetTVSelectionService(acquisition.NewTVSelectionService(postgres.NewTVSelectionStore(postgres.NewEpisodeMappingStore(postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(a.DBPool(), idgen.New())), idgen.New()))))
	}
	return httpapi.NewRouter(a, h, authSvc, nil)
}
func selectionPublicBody(r ports.TVSelectionRequest) string {
	packs, episodes := []map[string]any{}, []map[string]any{}
	for _, group := range []struct {
		refs []ports.TVSelectionCandidateRef
		out  *[]map[string]any
	}{{r.Packs, &packs}, {r.Episodes, &episodes}} {
		for _, ref := range group.refs {
			*group.out = append(*group.out, map[string]any{"candidate_id": ref.CandidateID, "review_id": ref.ReviewID})
		}
	}
	raw, _ := json.Marshal(map[string]any{"season": r.Season, "pack_policy": map[string]any{"mode": r.PackPolicy.Mode, "min_missing_ratio": r.PackPolicy.MinMissingRatio}, "packs": packs, "episodes": episodes})
	return string(raw)
}
func selectionPublicCall(h http.Handler, f *completeTVFixture, body, session string, edit func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/v1/subscriptions/"+f.f.sub.Id.String()+"/tv-selection-preview", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://"+r.Host)
	if session != "" {
		r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
	}
	if edit != nil {
		edit(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func selectionPublicResult(t *testing.T, w *httptest.ResponseRecorder, kind string) map[string]json.RawMessage {
	t.Helper()
	var b map[string]json.RawMessage
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" || json.Unmarshal(w.Body.Bytes(), &b) != nil {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var decision struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(b["decision"], &decision)
	if string(b["status"]) != `"preview_not_executable"` || string(b["execution_allowed"]) != "false" || decision.Kind != kind {
		t.Fatalf("unsafe or unexpected preview: %s", w.Body.String())
	}
	for _, field := range []string{"operation_id", "job_id", "receipt", "execution_authority"} {
		if _, ok := b[field]; ok {
			t.Fatal("authority returned")
		}
	}
	return b
}
func TestTVSelectionPublicPGStrictCookie(t *testing.T) {
	f, _, _, r := selectionFixture(t)
	h := selectionPublicRouter(f, true)
	body := selectionPublicBody(r)
	before := selectionState(t, f)
	cases := []struct {
		name, body, cookie string
		edit               func(*http.Request)
		want               int
	}{
		{"anonymous", body, "", nil, 401}, {"member", body, f.f.member, nil, 403},
		{"csrf", body, f.f.admin, func(r *http.Request) { r.Header.Set("Origin", "http://cross.invalid") }, 403},
		{"duplicate-origin", body, f.f.admin, func(r *http.Request) { r.Header.Add("Origin", r.Header.Get("Origin")) }, 403},
		{"duplicate-cookie", body, f.f.admin, func(r *http.Request) { r.Header.Add("Cookie", r.Header.Get("Cookie")) }, 400},
		{"duplicate-type", body, f.f.admin, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }, 400},
		{"wrong-type", body, f.f.admin, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 400},
		{"tail", body + " {}", f.f.admin, nil, 400}, {"empty", "", f.f.admin, nil, 400}, {"null", "null", f.f.admin, nil, 400},
		{"extra", strings.TrimSuffix(body, "}") + `,"execution_allowed":true}`, f.f.admin, nil, 400},
		{"case-alias", strings.Replace(body, `"season":1`, `"Season":1`, 1), f.f.admin, nil, 400},
		{"duplicate", strings.Replace(body, `"season":1`, `"season":1,"season":1`, 1), f.f.admin, nil, 400},
		{"policy-missing", strings.Replace(body, `"min_missing_ratio":1,`, "", 1), f.f.admin, nil, 400},
		{"policy-extra", strings.Replace(body, `"mode":`, `"score":999,"mode":`, 1), f.f.admin, nil, 400},
		{"policy-duplicate", strings.Replace(body, `"mode":`, `"mode":"none","mode":`, 1), f.f.admin, nil, 400},
		{"ref-extra", strings.Replace(body, `"review_id":`, `"unit_id":"`+uuid.NewString()+`","review_id":`, 1), f.f.admin, nil, 400},
		{"ref-duplicate", strings.Replace(body, `"candidate_id":`, `"candidate_id":"`+uuid.NewString()+`","candidate_id":`, 1), f.f.admin, nil, 400},
		{"null-packs", strings.Replace(body, `"packs":[{`, `"packs":null,"unknown":[{`, 1), f.f.admin, nil, 400},
		{"season-zero", strings.Replace(body, `"season":1`, `"season":0`, 1), f.f.admin, nil, 400},
		{"ratio-low", strings.Replace(body, `"min_missing_ratio":1`, `"min_missing_ratio":-1`, 1), f.f.admin, nil, 400},
		{"ratio-high", strings.Replace(body, `"min_missing_ratio":1`, `"min_missing_ratio":2`, 1), f.f.admin, nil, 400},
		{"missing-mode", strings.Replace(body, `"mode":"prefer_pack"`, `"mode":""`, 1), f.f.admin, nil, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			completePublicProblem(t, selectionPublicCall(h, f, tc.body, tc.cookie, tc.edit), tc.want)
		})
	}
	t.Run("total-bound", func(t *testing.T) {
		bad := r
		bad.Packs = nil
		bad.Episodes = nil
		for i := 0; i < 101; i++ {
			ref := ports.TVSelectionCandidateRef{CandidateID: uuid.New(), ReviewID: uuid.New()}
			if i < 51 {
				bad.Packs = append(bad.Packs, ref)
			} else {
				bad.Episodes = append(bad.Episodes, ref)
			}
		}
		completePublicProblem(t, selectionPublicCall(h, f, selectionPublicBody(bad), f.f.admin, nil), 400)
	})
	t.Run("duplicate-ref", func(t *testing.T) {
		bad := r
		bad.Episodes = append(bad.Episodes, r.Packs[0])
		completePublicProblem(t, selectionPublicCall(h, f, selectionPublicBody(bad), f.f.admin, nil), 400)
	})
	if before != selectionState(t, f) {
		t.Fatal("invalid preview mutated rows")
	}
	selectionPublicResult(t, selectionPublicCall(h, f, body, f.f.admin, nil), "pack")
}
func TestTVSelectionPublicPGAlternativesAndVersion(t *testing.T) {
	f, _, _, r := selectionFixture(t)
	selectionSingles(t, f, &r, []int{1, 2})
	h := selectionPublicRouter(f, true)
	before := selectionState(t, f)
	neutral := "pack"
	for _, ref := range r.Episodes {
		if ref.CandidateID.String() < r.Packs[0].CandidateID.String() {
			neutral = "episodes"
		}
	}
	for _, tc := range []struct{ mode, kind string }{{"prefer_pack", "pack"}, {"prefer_episodes", "episodes"}, {"require_pack", "pack"}, {"none", neutral}} {
		t.Run(tc.mode, func(t *testing.T) {
			r.PackPolicy.Mode = quality.PackMode(tc.mode)
			selectionPublicResult(t, selectionPublicCall(h, f, selectionPublicBody(r), f.f.admin, nil), tc.kind)
		})
	}
	r.PackPolicy.Mode = quality.PackMode("require_pack")
	r.Packs = nil
	selectionPublicResult(t, selectionPublicCall(h, f, selectionPublicBody(r), f.f.admin, nil), "hold")
	r.Packs = []ports.TVSelectionCandidateRef{{CandidateID: f.candidate, ReviewID: f.command.ReviewID}}
	raw := selectionPublicBody(r)
	b := selectionPublicResult(t, selectionPublicCall(h, f, raw, f.f.admin, nil), "pack")
	version := string(b["version"])
	fenced := strings.TrimSuffix(raw, "}") + `,"expected":` + version + `}`
	selectionPublicResult(t, selectionPublicCall(h, f, fenced, f.f.admin, nil), "pack")
	var changed map[string]any
	if err := json.Unmarshal(b["version"], &changed); err != nil {
		t.Fatal(err)
	}
	changed["intent_version"] = changed["intent_version"].(float64) + 1
	stale, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	completePublicProblem(t, selectionPublicCall(h, f, strings.TrimSuffix(raw, "}")+`,"expected":`+string(stale)+`}`, f.f.admin, nil), 409)
	for _, replacement := range []string{`"actor_id":"` + f.actor.ID + `","media_id":`, `"media_id":"` + uuid.NewString() + `","media_id":`} {
		bad := strings.Replace(fenced, `"media_id":`, replacement, 1)
		completePublicProblem(t, selectionPublicCall(h, f, bad, f.f.admin, nil), 400)
	}
	if before != selectionState(t, f) {
		t.Fatal("preview wrote persistent rows")
	}
}
func TestTVSelectionPublicPGConcurrentReadOnly(t *testing.T) {
	f, _, _, r := selectionFixture(t)
	h := selectionPublicRouter(f, true)
	before := selectionState(t, f)
	raw := selectionPublicBody(r)
	out := make(chan *httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); out <- selectionPublicCall(h, f, raw, f.f.admin, nil) }()
	}
	wg.Wait()
	close(out)
	for w := range out {
		selectionPublicResult(t, w, "pack")
	}
	if before != selectionState(t, f) {
		t.Fatal("concurrent previews wrote rows")
	}
}
func TestTVSelectionPublicPGRevocationAndToken(t *testing.T) {
	f, _, _, r := selectionFixture(t)
	h := selectionPublicRouter(f, true)
	raw := selectionPublicBody(r)
	tokenSvc := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.f.a.DBPool(), idgen.New()), crypto.ReadTokenIssuer{}, f.f.a.Clock())
	_, secret, e := tokenSvc.Create(context.Background(), f.actor, ports.TokenCommand{Name: "selection read"}, "selection-read")
	if e != nil {
		t.Fatal(e)
	}
	completePublicProblem(t, selectionPublicCall(h, f, raw, "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }), 403)
	before := selectionState(t, f)
	f.exec(t, "UPDATE app_user SET role='member' WHERE id=$1", uuid.MustParse(f.actor.ID))
	completePublicProblem(t, selectionPublicCall(h, f, raw, f.f.admin, nil), 403)
	if before != selectionState(t, f) {
		t.Fatal("revoked preview wrote rows")
	}
}
func TestTVSelectionPublicPGUnavailable(t *testing.T) {
	f, _, _, r := selectionFixture(t)
	raw := selectionPublicBody(r)
	completePublicProblem(t, selectionPublicCall(selectionPublicRouter(f, false), f, raw, f.f.admin, nil), 503)
	h := selectionPublicRouter(f, true)
	f.exec(t, "ALTER TABLE release_candidate RENAME TO private_selection_backend_failed")
	defer f.exec(t, "ALTER TABLE private_selection_backend_failed RENAME TO release_candidate")
	w := selectionPublicCall(h, f, raw, f.f.admin, nil)
	completePublicProblem(t, w, 503)
	if strings.Contains(w.Body.String(), "private_selection_backend_failed") {
		t.Fatal("backend details leaked")
	}
}

func TestTVSelectionPublicPGAccountWait(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "unchanged"
		if revoke {
			name = "revoked"
		}
		t.Run(name, func(t *testing.T) {
			f, _, _, r := selectionFixture(t)
			h := selectionPublicRouter(f, true)
			before := selectionState(t, f)
			tx, err := f.f.a.DBPool().Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err = tx.Exec(context.Background(), "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", uuid.MustParse(f.actor.ID)); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- selectionPublicCall(h, f, selectionPublicBody(r), f.f.admin, nil) }()
			waitCompleteTVLock(t, f, "app_user")
			if revoke {
				if _, err = tx.Exec(context.Background(), "UPDATE app_user SET role='member' WHERE id=$1", uuid.MustParse(f.actor.ID)); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			w := <-done
			if revoke {
				completePublicProblem(t, w, 503)
				completePublicProblem(t, selectionPublicCall(h, f, selectionPublicBody(r), f.f.admin, nil), 403)
			} else {
				selectionPublicResult(t, w, "pack")
			}
			if before != selectionState(t, f) {
				t.Fatal("account waiter wrote rows")
			}
		})
	}
}
