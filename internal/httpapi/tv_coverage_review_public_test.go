package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestTVCoverageReviewNativePGDownloaderSeal(t *testing.T) {
	f := newCoverageFixture(t)
	ctx := context.Background()
	before := f.preview(t)
	clientID := uuid.New()
	f.exec(t, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'coverage fixture','qbittorrent','http://private-client.invalid','private-user',$2,'[]')`, clientID, []byte("encrypted-fixture"))
	f.exec(t, `UPDATE subscription SET downloader_id=$1 WHERE id=$2`, clientID, f.f.sub.Id)
	v := f.preview(t)
	if !v.CanReview || v.MaterialStamp == before.MaterialStamp {
		t.Fatal("selected downloader not bound")
	}
	c := f.command(v)
	receipt, err := f.svc.Review(ctx, f.actor, c, "client-seal")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE download_client SET remote_path_map='[{"remote":"/download","local":"/media"}]' WHERE id=$1`, clientID)
	changed := f.preview(t)
	if changed.MaterialStamp == v.MaterialStamp {
		t.Fatal("changed downloader not sealed")
	}
	recovered, replayErr := f.svc.Review(ctx, f.actor, c, "client-seal")
	if replayErr != nil || recovered == nil || recovered.ID != receipt.ID {
		t.Fatal("historical review receipt lost", replayErr)
	}
	if _, err = f.svc.Review(ctx, f.actor, c, "client-seal-new"); !errors.Is(err, ports.ErrTVCoverageReviewConflict) {
		t.Fatal("changed client confirmation", err)
	}
}

func TestTVCoverageReviewNativePGPublicCookieAndProblems(t *testing.T) {
	f := newCoverageFixture(t)
	base := "/api/v1/subscriptions/" + f.f.sub.Id.String() + "/tv-coverage-candidate-reviews"
	call := func(path, raw string, keys []string, cookie, crossSite bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://"+req.Host)
		if cookie {
			req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: f.f.admin})
		}
		if crossSite {
			req.Header.Set("Origin", "http://cross-site.invalid")
		}
		for _, key := range keys {
			req.Header.Add("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, req)
		return w
	}
	previewRaw, _ := json.Marshal(map[string]any{"candidate_id": f.candidate})
	w := call(base+"/preview", string(previewRaw), nil, true, false)
	var observation ports.TVCoverageCandidateReviewObservation
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &observation) != nil || !observation.CanReview || observation.ExecutionAllowed || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("public cookie preview", w.Code, w.Body.String())
	}
	raw, _ := json.Marshal(map[string]any{"candidate_id": f.candidate, "expected_input_stamp": observation.InputStamp, "confirmed": true, "reason": "whole-set identification"})
	counts := f.counts(t)
	for _, tc := range []struct {
		name, path, raw   string
		keys              []string
		cookie, crossSite bool
		want              int
	}{
		{"unauthorized", base + "/preview", string(previewRaw), nil, false, false, 401},
		{"csrf", base, string(raw), []string{"csrf"}, true, true, 403},
		{"no-key", base, string(raw), nil, true, false, 400},
		{"duplicate-key", base, string(raw), []string{"one", "two"}, true, false, 400},
		{"trailing-json", base, string(raw) + " {}", []string{"tail"}, true, false, 400},
		{"unknown-authority", base, string(raw[:len(raw)-1]) + `,"unit_ids":[]}`, []string{"unknown"}, true, false, 400},
		{"preview-authority", base + "/preview", `{"candidate_id":"` + f.candidate.String() + `","execution_allowed":true}`, nil, true, false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := call(tc.path, tc.raw, tc.keys, tc.cookie, tc.crossSite)
			if got.Code != tc.want || got.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatal(got.Code, got.Body.String())
			}
			var p map[string]any
			if json.Unmarshal(got.Body.Bytes(), &p) != nil || p["status"] != float64(tc.want) || p["type"] == nil || p["title"] == nil || p["detail"] == nil {
				t.Fatal("RFC9457", got.Body.String())
			}
			if f.counts(t) != counts {
				t.Fatal("invalid public request wrote work or review")
			}
		})
	}
	w = call(base, string(raw), []string{"cookie-confirm"}, true, false)
	var receipt ports.TVCoverageCandidateReviewReceipt
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Status != "reviewed_not_executable" {
		t.Fatal("public confirmation", w.Code, w.Body.String())
	}
	recovered := call(base, string(raw), []string{"cookie-confirm"}, true, false)
	var replay ports.TVCoverageCandidateReviewReceipt
	if recovered.Code != 200 || json.Unmarshal(recovered.Body.Bytes(), &replay) != nil || replay.ID != receipt.ID {
		t.Fatal("original public recovery", recovered.Code, recovered.Body.String())
	}
	f.exec(t, `UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1`, f.candidate)
	w = call(base, string(raw), []string{"cookie-confirm"}, true, false)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &replay) != nil || replay.ID != receipt.ID {
		t.Fatal("historical public receipt lost", w.Code, w.Body.String())
	}
	w = call(base, string(raw), []string{"stale-new-confirmation"}, true, false)
	if w.Code != 409 || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatal("stale public confirmation", w.Code, w.Body.String())
	}
	f.exec(t, `ALTER TABLE tv_season_metadata_snapshot RENAME TO coverage_unavailable_fixture`)
	t.Cleanup(func() { f.exec(t, `ALTER TABLE coverage_unavailable_fixture RENAME TO tv_season_metadata_snapshot`) })
	w = call(base+"/preview", string(previewRaw), nil, true, false)
	if w.Code != 503 || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatal("public unavailable", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("coverage_unavailable_fixture")) || bytes.Contains(w.Body.Bytes(), []byte("private-client")) {
		t.Fatal("raw infrastructure error leaked")
	}
	final := f.counts(t)
	if final[0] != counts[0] || final[1] != counts[1] || final[2] != counts[2] || final[3] != counts[3] || final[4] != counts[4] || final[5] != counts[5] || final[6] != counts[6]+1 {
		t.Fatal("review manufactured execution", final, counts)
	}
}
