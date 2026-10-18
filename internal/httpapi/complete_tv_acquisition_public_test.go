package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

func completePublicRouter(a *app.App, configured bool) http.Handler {
	authSvc := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	h := httpapi.NewServerHandler(a, authSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if configured {
		h.SetCompleteTVAcquisitionService(acquisition.NewCompleteTVService(postgres.NewCompleteTVAcquisitionStore(a.DBPool(), idgen.New())))
	}
	return httpapi.NewRouter(a, h, authSvc, nil)
}
func completePublicBody(f *completeTVFixture) string {
	raw, _ := json.Marshal(map[string]any{"review_id": f.command.ReviewID, "expected_input_stamp": f.command.ExpectedInputStamp, "expected_material_stamp": f.command.ExpectedMaterialStamp, "confirmed": true})
	return string(raw)
}
func completePublicCall(router http.Handler, f *completeTVFixture, raw, key, session string, edit func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/subscriptions/"+f.f.sub.Id.String()+"/complete-tv-acquisitions", strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
	}
	if edit != nil {
		edit(req)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
func completePublicProblem(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	var p map[string]any
	if w.Code != want || w.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || json.Unmarshal(w.Body.Bytes(), &p) != nil || p["status"] != float64(want) || p["type"] == nil || p["title"] == nil || p["detail"] == nil {
		t.Fatalf("problem want %d: %d %s headers=%v", want, w.Code, w.Body.String(), w.Header())
	}
}
func completePublicReceipt(t *testing.T, w *httptest.ResponseRecorder) ports.AcquireResult {
	t.Helper()
	var r ports.AcquireResult
	if w.Code != 202 || w.Header().Get("Cache-Control") != "private, no-store" || json.Unmarshal(w.Body.Bytes(), &r) != nil || r.OperationID == uuid.Nil || r.JobID == uuid.Nil || len(r.Reservations) != 2 {
		t.Fatalf("receipt: %d %s", w.Code, w.Body.String())
	}
	return r
}
func TestCompleteTVPublicPGStrictCookie(t *testing.T) {
	f := newCompleteTVFixture(t)
	router := completePublicRouter(f.f.a, true)
	raw := completePublicBody(f)
	before := f.work(t)
	cases := []struct {
		name, body, key, cookie string
		edit                    func(*http.Request)
		want                    int
	}{
		{"anonymous", raw, "anon", "", nil, 401},
		{"member", raw, "member", f.f.member, nil, 403},
		{"csrf", raw, "csrf", f.f.admin, func(r *http.Request) { r.Header.Set("Origin", "http://cross-site.invalid") }, 403},
		{"no-key", raw, "", f.f.admin, nil, 400},
		{"duplicate-key", raw, "key", f.f.admin, func(r *http.Request) { r.Header.Add("Idempotency-Key", "key2") }, 400},
		{"comma-key", raw, "key,key2", f.f.admin, nil, 400},
		{"duplicate-content-type", raw, "type", f.f.admin, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }, 400},
		{"duplicate-origin", raw, "origin", f.f.admin, func(r *http.Request) { r.Header.Add("Origin", r.Header.Get("Origin")) }, 403},
		{"duplicate-cookie", raw, "cookie", f.f.admin, func(r *http.Request) { r.Header.Add("Cookie", r.Header.Get("Cookie")) }, 400},
		{"trailing", raw + " {}", "tail", f.f.admin, nil, 400},
		{"empty", "", "empty", f.f.admin, nil, 400},
		{"null", "null", "null", f.f.admin, nil, 400},
		{"array", "[]", "array", f.f.admin, nil, 400},
		{"false", strings.Replace(raw, "true", "false", 1), "false", f.f.admin, nil, 400},
		{"missing-material", strings.Replace(raw, `"expected_material_stamp":"`+f.command.ExpectedMaterialStamp+`",`, "", 1), "missing", f.f.admin, nil, 400},
		{"null-material", strings.Replace(raw, `"`+f.command.ExpectedMaterialStamp+`"`, "null", 1), "null-material", f.f.admin, nil, 400},
		{"uppercase-stamp", strings.Replace(raw, f.command.ExpectedInputStamp, strings.ToUpper(f.command.ExpectedInputStamp), 1), "upper", f.f.admin, nil, 400},
		{"long-key", raw, strings.Repeat("a", 131), f.f.admin, nil, 400},
	}
	for _, field := range []string{"subscription_id", "unit_ids", "root_id", "downloader_id", "intent_version", "scope", "execution_allowed", "Review_id"} {
		cases = append(cases, struct {
			name, body, key, cookie string
			edit                    func(*http.Request)
			want                    int
		}{"unknown-" + field, raw[:len(raw)-1] + `,"` + field + `":true}`, field, f.f.admin, nil, 400})
	}
	for _, field := range []string{"review_id", "expected_input_stamp", "expected_material_stamp", "confirmed"} {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &fields)
		cases = append(cases, struct {
			name, body, key, cookie string
			edit                    func(*http.Request)
			want                    int
		}{"duplicate-" + field, raw[:len(raw)-1] + `,"` + field + `":` + string(fields[field]) + `}`, "dup-" + field, f.f.admin, nil, 400})
	}
	cases = append(cases, struct {
		name, body, key, cookie string
		edit                    func(*http.Request)
		want                    int
	}{"escaped-duplicate", raw[:len(raw)-1] + `,"confi\u0072med":true}`, "escape", f.f.admin, nil, 400})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			completePublicProblem(t, completePublicCall(router, f, tc.body, tc.key, tc.cookie, tc.edit), tc.want)
			if f.work(t) != before {
				t.Fatal("rejected command wrote work")
			}
		})
	}
	completePublicProblem(t, completePublicCall(completePublicRouter(f.f.a, false), f, raw, "no-service", f.f.admin, nil), 503)
}

func TestCompleteTVPublicPGConcurrentRecoveryAndRestart(t *testing.T) {
	f := newCompleteTVFixture(t)
	router := completePublicRouter(f.f.a, true)
	raw := completePublicBody(f)
	var results [8]*httptest.ResponseRecorder
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = completePublicCall(router, f, raw, "lost-receipt", f.f.admin, nil)
		}(i)
	}
	wg.Wait()
	first := completePublicReceipt(t, results[0])
	if first.Status != "queued" {
		t.Fatal(first.Status)
	}
	for _, w := range results {
		r := completePublicReceipt(t, w)
		if r.OperationID != first.OperationID || r.JobID != first.JobID || !bytes.Equal(w.Body.Bytes(), results[0].Body.Bytes()) {
			t.Fatal("duplicate operation/receipt")
		}
	}
	if got := f.work(t); got != [9]int{1, 2, 2, 2, 1, 1, 1, 1, 0} {
		t.Fatal("not atomic or synchronous execution", got)
	}
	completePublicReceipt(t, completePublicCall(router, f, raw, "lost-receipt", f.f.admin, nil))
	cfg := f.f.a.Config()
	if err := f.f.a.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	f.f.a = restarted
	router = completePublicRouter(restarted, true)
	w := completePublicCall(router, f, raw, "lost-receipt", f.f.admin, nil)
	replay := completePublicReceipt(t, w)
	if replay.OperationID != first.OperationID || !bytes.Equal(w.Body.Bytes(), results[0].Body.Bytes()) {
		t.Fatal("restart lost historical receipt")
	}
	for _, field := range []string{"expected_input_stamp", "expected_material_stamp", "review_id"} {
		t.Run("changed-command-"+field, func(t *testing.T) {
			var fields map[string]any
			_ = json.Unmarshal([]byte(raw), &fields)
			fields[field] = strings.Repeat("f", 64)
			if field == "review_id" {
				fields[field] = uuid.New().String()
			}
			changed, _ := json.Marshal(fields)
			completePublicProblem(t, completePublicCall(router, f, string(changed), "lost-receipt", f.f.admin, nil), 409)
		})
	}
	f.exec(t, `UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1`, f.candidate)
	w = completePublicCall(router, f, raw, "lost-receipt", f.f.admin, nil)
	completePublicReceipt(t, w)
	if !bytes.Equal(w.Body.Bytes(), results[0].Body.Bytes()) {
		t.Fatal("material change rewrote receipt")
	}
	completePublicProblem(t, completePublicCall(router, f, raw, "new-stale", f.f.admin, nil), 409)
	f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.actor.ID))
	completePublicProblem(t, completePublicCall(router, f, raw, "lost-receipt", f.f.admin, nil), 403)
}

func TestCompleteTVPublicPGLegacyReceiptPreserved(t *testing.T) {
	f := newCompleteTVFixture(t)
	router := completePublicRouter(f.f.a, true)
	raw := completePublicBody(f)
	r := completePublicReceipt(t, completePublicCall(router, f, raw, "historical", f.f.admin, nil))
	f.exec(t, `UPDATE idempotency_record SET response_body=jsonb_set(response_body,'{status}','"reserved_not_executable"') WHERE idempotency_key='complete-tv.acquire:historical'`)
	before := f.work(t)
	replay := completePublicReceipt(t, completePublicCall(router, f, raw, "historical", f.f.admin, nil))
	if replay.Status != "reserved_not_executable" || replay.OperationID != r.OperationID || f.work(t) != before {
		t.Fatal("historical receipt changed", replay)
	}
	next := completePublicReceipt(t, completePublicCall(router, f, raw, "new-exact-authority", f.f.admin, nil))
	if next.Status != "queued" || next.OperationID != r.OperationID {
		t.Fatal("new reuse receipt", next)
	}
}

func TestCompleteTVPublicPGChangedEvidenceAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		want      int
	}{
		{"material", `UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1`, 409},
		{"input", `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE media_id=(SELECT media_id FROM subscription WHERE id=$2)`, 409},
		{"scope", `UPDATE wanted_item SET is_skipped=true,user_skipped=true WHERE subscription_id=$2`, 409},
		{"client", `UPDATE download_client SET remote_path_map='[{"remote":"/changed","local":"/changed"}]' WHERE id=(SELECT downloader_id FROM subscription WHERE id=$2)`, 409},
		{"unavailable", `ALTER TABLE complete_tv_execution_authority RENAME TO private_public_api_unavailable`, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCompleteTVFixture(t)
			router := completePublicRouter(f.f.a, true)
			raw := completePublicBody(f)
			before := f.work(t)
			if tc.name == "unavailable" {
				f.exec(t, tc.sql)
				defer f.exec(t, `ALTER TABLE private_public_api_unavailable RENAME TO complete_tv_execution_authority`)
			} else {
				f.exec(t, `WITH fixture AS (SELECT $1::uuid,$2::uuid) `+tc.sql, f.candidate, f.f.sub.Id)
			}
			w := completePublicCall(router, f, raw, tc.name, f.f.admin, nil)
			completePublicProblem(t, w, tc.want)
			if strings.Contains(w.Body.String(), "private_public_api_unavailable") {
				t.Fatal("SQL leaked")
			}
			if tc.name != "unavailable" && f.work(t) != before {
				t.Fatal("conflict leaked writes")
			}
		})
	}
}

func TestCompleteTVPublicPGAccountRevalidation(t *testing.T) {
	for _, mode := range []string{"disabled", "demoted"} {
		t.Run(mode, func(t *testing.T) {
			f := newCompleteTVFixture(t)
			router := completePublicRouter(f.f.a, true)
			raw := completePublicBody(f)
			before := f.work(t)
			tx, err := f.f.a.DBPool().Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			sql := `UPDATE app_user SET is_active=false WHERE id=$1`
			if mode == "demoted" {
				sql = `UPDATE app_user SET role='member' WHERE id=$1`
			}
			if _, err = tx.Exec(context.Background(), sql, uuid.MustParse(f.actor.ID)); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- completePublicCall(router, f, raw, "account-wait", f.f.admin, nil) }()
			waitCompleteTVLock(t, f, "app_user")
			if err = tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			completePublicProblem(t, <-done, 403)
			if f.work(t) != before {
				t.Fatal("revocation wrote authority")
			}
		})
	}
}

func TestCompleteTVPublicPGAPITokenCannotRecover(t *testing.T) {
	f := newCompleteTVFixture(t)
	router := completePublicRouter(f.f.a, true)
	raw := completePublicBody(f)
	completePublicReceipt(t, completePublicCall(router, f, raw, "session-only", f.f.admin, nil))
	before := f.work(t)
	tokenSvc := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.f.a.DBPool(), idgen.New()), crypto.ReadTokenIssuer{}, f.f.a.Clock())
	_, secret, err := tokenSvc.Create(context.Background(), f.actor, ports.TokenCommand{Name: "complete public read"}, "public-read-token")
	if err != nil {
		t.Fatal(err)
	}
	w := completePublicCall(router, f, raw, "session-only", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) })
	completePublicProblem(t, w, 403)
	if f.work(t) != before {
		t.Fatal("API credential read or wrote command")
	}
}
