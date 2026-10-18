package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTVCoverageReviewNativePGActorAndFactIsolation(t *testing.T) {
	f := newCoverageFixture(t)
	ctx := context.Background()
	v := f.preview(t)
	c := f.command(v)
	f.exec(t, "UPDATE media_unit SET facts_version=facts_version+1 WHERE id=$1", f.f.units[0])
	changed := f.preview(t)
	if v.MaterialStamp != changed.MaterialStamp || v.InputStamp == changed.InputStamp {
		t.Fatal("mutable facts contaminated material stamp")
	}
	for _, a := range []*ports.Actor{nil, {Type: ports.ActorTypeSystem, ID: f.actor.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeService, ID: f.actor.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleAdmin, Scopes: []string{"admin"}}, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleMember}} {
		_, e := f.svc.Review(ctx, a, c, "forbidden")
		if !errors.Is(e, ports.ErrForbidden) && !errors.Is(e, ports.ErrUnauthorized) {
			t.Fatal("invalid actor accepted", a, e)
		}
	}
	c = f.command(changed)
	r, e := f.svc.Review(ctx, f.actor, c, "actor-receipt")
	if e != nil || r == nil {
		t.Fatal(e)
	}
	before := f.counts(t)
	f.exec(t, "UPDATE app_user SET role='member' WHERE id=$1", uuid.MustParse(f.actor.ID))
	if _, e = f.svc.Review(ctx, f.actor, c, "actor-receipt"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("demoted actor read cached receipt", e)
	}
	if f.counts(t) != before {
		t.Fatal("invalid replay wrote state")
	}
	f.exec(t, "UPDATE app_user SET role='admin',is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID))
	if _, e = f.svc.Review(ctx, f.actor, c, "actor-receipt"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("revoked actor read cached receipt", e)
	}
}
func TestTVCoverageReviewNativePGRecomputeAfterRootWait(t *testing.T) {
	f := newCoverageFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := f.f.a.DBPool()
	c := f.command(f.preview(t))
	tx, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(ctx, "SELECT id FROM library_root WHERE id=(SELECT root_id FROM subscription WHERE id=$1) FOR UPDATE", f.f.sub.Id); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.svc.Review(ctx, f.actor, c, "root-wait"); done <- e }()
	waiting := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e = p.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%library_root%FOR%UPDATE%')").Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("root wait was not observed")
	}
	f.exec(t, "UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1", f.candidate)
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, ports.ErrTVCoverageReviewConflict) {
		t.Fatal("pre-lock evidence reused", e)
	}
	if f.counts(t)[6] != 0 {
		t.Fatal("stale review survived")
	}
}
func TestTVCoverageReviewNativePGPrivateHTTPContract(t *testing.T) {
	f := newCoverageFixture(t)
	base := "/api/v1/subscriptions/" + f.f.sub.Id.String() + "/tv-coverage-candidate-reviews"
	call := func(token, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, req)
		return w
	}
	for _, token := range []string{"", f.f.member} {
		w := call(token, base+"/preview", "", map[string]any{"candidate_id": f.candidate})
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("preview auth", w.Code)
		}
	}
	w := call(f.f.admin, base+"/preview", "", map[string]any{"candidate_id": f.candidate})
	var v ports.TVCoverageCandidateReviewObservation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || !v.CanReview || v.ExecutionAllowed || len(v.Rows) != 2 {
		t.Fatal("private preview", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("fixture.invalid")) || bytes.Contains(w.Body.Bytes(), []byte("Material")) {
		t.Fatal("material secret leak")
	}
	w = call(f.f.admin, base, "http-unknown", map[string]any{"candidate_id": f.candidate, "expected_input_stamp": v.InputStamp, "confirmed": true, "reason": "review only", "unit_ids": f.f.units})
	if w.Code != 400 {
		t.Fatal("unknown fields accepted", w.Code)
	}
	w = call(f.f.admin, base, "http-review", map[string]any{"candidate_id": f.candidate, "expected_input_stamp": v.InputStamp, "confirmed": true, "reason": "review only"})
	if w.Code != 200 {
		t.Fatal("private review", w.Code, w.Body.String())
	}
	// The canonical generated route must be registered and validate malformed requests.
	shared := f.f.call(f.f.admin, "POST", base, "public-malformed", nil)
	if shared.Code != 400 || shared.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatal("public registration or problem contract", shared.Code, shared.Body.String())
	}
}
