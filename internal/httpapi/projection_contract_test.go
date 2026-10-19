package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProjectionContractRejectsLegacyQualityResult(t *testing.T) {
	for _, testcase := range []struct {
		name string
		raw  []byte
	}{
		{"unversioned", []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray"}`)},
		{"superseded-v1", []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v1","codec":"HEVC","codec_parser_version":"codec.tokens.v1"}`)},
	} {
		t.Run(testcase.name, func(t *testing.T) { testProjectionContractRejectsLegacyQualityResult(t, testcase.raw) })
	}
}

func testProjectionContractRejectsLegacyQualityResult(t *testing.T, original []byte) {
	a, _, token, root, _, _, router, _ := setupOperationHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q, pool := a.Queries(), a.DBPool()
	mediaID, unitID, fileID := uuid.New(), uuid.New(), uuid.New()
	if _, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Legacy source projection", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: "movie"}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(httpapi.CreateSubscriptionRequest{MediaId: &mediaID, RootId: &root})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/subscriptions", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	var sub httpapi.SubscriptionDetails
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &sub) != nil || sub.WantedItems == nil || len(*sub.WantedItems) != 1 {
		t.Fatal("create fixture", response.Code)
	}
	wantedID := (*sub.WantedItems)[0].Id
	if _, err = q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: fileID, RootID: root, RelPath: "legacy-quality.mkv", SizeBytes: 100, MtimeNs: 1}); err != nil {
		t.Fatal(err)
	}
	if err = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: fileID, UnitID: unitID}); err != nil {
		t.Fatal(err)
	}
	// Original unversioned or superseded source evidence must not be re-signed.
	factID := uuid.New()
	if _, err = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: factID, UnitID: unitID, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: fileID, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: original}); err != nil {
		t.Fatal(err)
	}
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(pool))
	report, err := evaluator.EvaluateWantedItem(ctx, wantedID, quality.DefaultPolicy())
	if err != nil || report.IsSatisfied || report.WaitReason != "quality_unknown" {
		t.Fatal("current ownership must hold", report, err)
	}
	var factsVersion int64
	if err = pool.QueryRow(ctx, "SELECT facts_version FROM media_unit WHERE id=$1", unitID).Scan(&factsVersion); err != nil {
		t.Fatal(err)
	}
	// Emulate a previous binary updating its cached outcome without knowing the
	// new evaluator contract. Entity, intent, policy and fact stamps all remain current.
	if _, err = pool.Exec(ctx, `UPDATE wanted_item SET is_satisfied=true,current_fact_stage='imported',next_evaluate_at=clock_timestamp()+interval '1 year' WHERE id=$1`, wantedID); err != nil {
		t.Fatal(err)
	}
	var current bool
	if err = pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wantedID).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current {
		t.Error("legacy evaluator result still passes full input stamp")
	}
	req = httptest.NewRequest("GET", "/api/v1/subscriptions/"+sub.Id.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	var details httpapi.SubscriptionDetails
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &details) != nil || details.WantedItems == nil || len(*details.WantedItems) != 1 {
		t.Fatal("read detail", response.Code)
	}
	item := (*details.WantedItems)[0]
	if item.IsSatisfied || item.CurrentFactStage != nil {
		t.Error("legacy unknown-quality projection still emits green imported", item)
	}
	due, err := q.ListPendingEvaluationWantedItems(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range due {
		if row.ID == wantedID {
			found = true
		}
	}
	if !found {
		t.Error("contract change did not wake recomputation despite future schedule")
	}
	var retained []byte
	var versionAfter int64
	var files, mappings int
	if err = pool.QueryRow(ctx, "SELECT quality_profile FROM unit_fact WHERE id=$1", factID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	var before, after any
	if json.Unmarshal(original, &before) != nil || json.Unmarshal(retained, &after) != nil {
		t.Fatal("decode retained evidence")
	}
	// Compare canonical JSON bytes; PostgreSQL JSONB only normalizes formatting.
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	if !bytes.Equal(b1, b2) {
		t.Error("legacy facts rewritten")
	}
	if err = pool.QueryRow(ctx, "SELECT facts_version FROM media_unit WHERE id=$1", unitID).Scan(&versionAfter); err != nil || versionAfter != factsVersion {
		t.Fatal("projection changed source version", versionAfter, err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM library_file WHERE id=$1", fileID).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM library_file_unit WHERE file_id=$1 AND unit_id=$2", fileID, unitID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if files != 1 || mappings != 1 {
		t.Fatal("projection gate removed real file or mapping")
	}

	// A new evaluator recovers its own contract, but never upgrades legacy evidence.
	report, err = evaluator.EvaluateWantedItem(ctx, wantedID, quality.DefaultPolicy())
	if err != nil || report.IsSatisfied || report.WaitReason != "quality_unknown" {
		t.Fatal("legacy fact must stay unknown after recovery", report, err)
	}
	var version string
	var token1, token2 uuid.UUID
	if err = pool.QueryRow(ctx, "SELECT evaluated_contract_version,evaluated_write_token FROM wanted_item WHERE id=$1", wantedID).Scan(&version, &token1); err != nil || version != quality.OwnershipContractVersion || token1 == uuid.Nil {
		t.Fatal("current projector missing authority", version, token1, err)
	}
	if _, err = evaluator.EvaluateWantedItem(ctx, wantedID, quality.DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT evaluated_write_token FROM wanted_item WHERE id=$1", wantedID).Scan(&token2); err != nil || token2 == token1 {
		t.Fatal("reprojection reused write token", err)
	}
	if err = pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wantedID).Scan(&current); err != nil || !current {
		t.Fatal("current unknown result lost stamp", current, err)
	}
	// Rolled-back old writes do not invalidate a committed current result.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "UPDATE wanted_item SET is_satisfied=true WHERE id=$1", wantedID); err != nil {
		t.Fatal(err)
	}
	var inside bool
	if err = tx.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wantedID).Scan(&inside); err != nil || inside {
		t.Fatal("legacy write not invalidated inside tx", inside, err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wantedID).Scan(&current); err != nil || !current {
		t.Fatal("rollback invalidated original stamp", current, err)
	}
	// Append a genuinely new versioned fact rather than rewriting the old JSON.
	valid, err := json.Marshal(quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unitID, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: fileID, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: valid}); err != nil {
		t.Fatal(err)
	}
	report, err = evaluator.EvaluateWantedItem(ctx, wantedID, quality.DefaultPolicy())
	if err != nil || !report.IsSatisfied {
		t.Fatal("valid evidence cannot recover projection", report, err)
	}
	req = httptest.NewRequest("GET", "/api/v1/subscriptions/"+sub.Id.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &details) != nil || details.WantedItems == nil || !(*details.WantedItems)[0].IsSatisfied {
		t.Fatal("current versioned fact not green", response.Code)
	}
	// Even an old no-op write must not retain the current evaluator authority.
	if _, err = pool.Exec(ctx, "UPDATE wanted_item SET is_satisfied=is_satisfied WHERE id=$1", wantedID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wantedID).Scan(&current); err != nil || current {
		t.Fatal("old no-op writer retained new authority", current, err)
	}
	req = httptest.NewRequest("GET", "/api/v1/subscriptions/"+sub.Id.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &details) != nil || details.WantedItems == nil || (*details.WantedItems)[0].IsSatisfied {
		t.Fatal("invalidated no-op result stayed green", response.Code)
	}
	if _, err = evaluator.EvaluateWantedItem(ctx, wantedID, quality.DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	// A fresh token for the wrong semantic version must never pass the DB view.
	if _, err = pool.Exec(ctx, "UPDATE wanted_item SET evaluated_contract_version='ownership.quality.future',evaluated_write_token=gen_random_uuid() WHERE id=$1", wantedID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wantedID).Scan(&current); err != nil || current {
		t.Fatal("unrecognized contract accepted", current, err)
	}
}
