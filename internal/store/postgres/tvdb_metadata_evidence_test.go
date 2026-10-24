package postgres_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func seedTVDBEvidence(t *testing.T, f creationFixture) db.BindTVDBEpisodeEvidenceParams {
	t.Helper()
	ctx := t.Context()
	q := db.New(f.pool)
	media, unit := uuid.New(), uuid.New()
	if _, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Evidence fixture", MediaType: "tv"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: media, Namespace: "tmdb.tv", ExternalID: "1396"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ApplyTVEpisodeMetadata(ctx, db.ApplyTVEpisodeMetadataParams{ID: unit, MediaID: media, UnitKey: "S01E107", SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: 107, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.BindTMDBEpisodeIdentity(ctx, db.BindTMDBEpisodeIdentityParams{UnitID: unit, ExternalID: 701, TmdbSeriesID: 1396}); err != nil {
		t.Fatal(err)
	}
	// This schema fixture exercises opaque storage only; encryption is verified by settings tests.
	for key, value := range map[string]string{"tvdb.credentials": `{"ciphertext":"b3duZWQtc2NoZW1hLWZpeHR1cmU="}`, "tvdb.fallback": `{"enabled":true}`} {
		if _, err := q.SetSetting(ctx, db.SetSettingParams{Key: key, Value: json.RawMessage(value)}); err != nil {
			t.Fatal(err)
		}
	}
	return db.BindTVDBEpisodeEvidenceParams{UnitID: unit, MediaID: media, TmdbSeriesID: 1396, TvdbSeriesID: 81189, TmdbEpisodeID: 701, TvdbEpisodeID: 9002, SeasonNumber: 1, EpisodeNumber: 107, CredentialVersion: 1, FallbackVersion: 1}
}
func bindTVDBEvidence(ctx context.Context, q *db.Queries, p db.BindTVDBEpisodeEvidenceParams) error {
	if _, err := q.BindTVDBSeriesIdentity(ctx, db.BindTVDBSeriesIdentityParams{MediaID: p.MediaID, TmdbSeriesID: p.TmdbSeriesID, TvdbSeriesID: p.TvdbSeriesID}); err != nil {
		return err
	}
	_, err := q.BindTVDBEpisodeEvidence(ctx, p)
	return err
}
func tvdbEvidenceCounts(t *testing.T, f creationFixture) [6]int {
	t.Helper()
	var counts [6]int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM tvdb_series_identity),(SELECT count(*) FROM tvdb_episode_evidence),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM idempotency_record),(SELECT count(*) FROM job)`).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5]); err != nil {
		t.Fatal(err)
	}
	return counts
}
func TestTVDBEvidenceSchema117RetirementProtectionPG(t *testing.T) {
	f := newCreationFixture(t, 117)
	p := seedTVDBEvidence(t, f)
	q := db.New(f.pool)
	if err := bindTVDBEvidence(t.Context(), q, p); err != nil {
		t.Fatal(err)
	}
	other := uuid.New()
	if _, err := q.ApplyTVEpisodeMetadata(t.Context(), db.ApplyTVEpisodeMetadataParams{ID: other, MediaID: p.MediaID, UnitKey: "S01E02", SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: 2, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := q.RetireMissingTVSeasonUnits(t.Context(), db.RetireMissingTVSeasonUnitsParams{MediaID: p.MediaID, SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, Column3: []int32{1}}); err != nil {
		t.Fatal(err)
	}
	var protected, ordinary bool
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT retired FROM media_unit WHERE id=$1),(SELECT retired FROM media_unit WHERE id=$2)`, p.UnitID, other).Scan(&protected, &ordinary); err != nil || protected || !ordinary {
		t.Fatal("supplement lost or ordinary retirement changed", err)
	}
	proof, err := q.GetTVDBEpisodeEvidence(t.Context(), p.UnitID)
	if err != nil || proof.EpisodeNumber != 107 || proof.TmdbEpisodeID != 701 || proof.TvdbEpisodeID != 9002 {
		t.Fatal("three-digit canonical identity lost", err)
	}
	if tvdbEvidenceCounts(t, f) != ([6]int{1, 1, 0, 0, 0, 0}) {
		t.Fatal("identity binding started business work")
	}
}
func TestTVDBEvidenceSchema117RejectsUnprovenBindingPG(t *testing.T) {
	f := newCreationFixture(t, 117)
	p := seedTVDBEvidence(t, f)
	q := db.New(f.pool)
	if _, err := q.BindTVDBSeriesIdentity(t.Context(), db.BindTVDBSeriesIdentityParams{MediaID: p.MediaID, TmdbSeriesID: 1397, TvdbSeriesID: 81189}); err == nil {
		t.Fatal("foreign canonical series accepted")
	}
	if _, err := q.BindTVDBSeriesIdentity(t.Context(), db.BindTVDBSeriesIdentityParams{MediaID: p.MediaID, TmdbSeriesID: 1396, TvdbSeriesID: 81189}); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*db.BindTVDBEpisodeEvidenceParams){
		"foreign_unit":          func(p *db.BindTVDBEpisodeEvidenceParams) { p.UnitID = uuid.New() },
		"foreign_media":         func(p *db.BindTVDBEpisodeEvidenceParams) { p.MediaID = uuid.New() },
		"foreign_series":        func(p *db.BindTVDBEpisodeEvidenceParams) { p.TmdbSeriesID = 1397 },
		"foreign_source_series": func(p *db.BindTVDBEpisodeEvidenceParams) { p.TvdbSeriesID = 81190 },
		"foreign_episode":       func(p *db.BindTVDBEpisodeEvidenceParams) { p.TmdbEpisodeID = 702 },
		"different_coordinate":  func(p *db.BindTVDBEpisodeEvidenceParams) { p.EpisodeNumber = 7 },
		"old_credentials":       func(p *db.BindTVDBEpisodeEvidenceParams) { p.CredentialVersion = 2 },
		"old_switch":            func(p *db.BindTVDBEpisodeEvidenceParams) { p.FallbackVersion = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := p
			change(&bad)
			if _, err := q.BindTVDBEpisodeEvidence(t.Context(), bad); err == nil {
				t.Fatal("unproven binding accepted")
			}
		})
	}
	if _, err := q.SetSetting(t.Context(), db.SetSettingParams{Key: "tvdb.fallback", Value: []byte(`{"enabled":false}`)}); err != nil {
		t.Fatal(err)
	}
	p.FallbackVersion = 2
	if _, err := q.BindTVDBEpisodeEvidence(t.Context(), p); err == nil {
		t.Fatal("disabled fallback issued evidence")
	}
	if tvdbEvidenceCounts(t, f) != ([6]int{1, 0, 0, 0, 0, 0}) {
		t.Fatal("rejected bindings left writes")
	}
}
func TestTVDBEvidenceSchema117ConcurrentBindingPG(t *testing.T) {
	f := newCreationFixture(t, 117)
	p := seedTVDBEvidence(t, f)
	q := db.New(f.pool)
	if _, err := q.BindTVDBSeriesIdentity(t.Context(), db.BindTVDBSeriesIdentityParams{MediaID: p.MediaID, TmdbSeriesID: 1396, TvdbSeriesID: 81189}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := q.BindTVDBEpisodeEvidence(t.Context(), p); results <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if tvdbEvidenceCounts(t, f) != ([6]int{1, 1, 0, 0, 0, 0}) {
		t.Fatal("duplicate identity evidence")
	}
	changed := p
	changed.TvdbEpisodeID = 9999
	if count, err := q.BindTVDBEpisodeEvidence(t.Context(), changed); err != nil || count != 0 {
		t.Fatal("conflicting source rewrote identity", count, err)
	}
	proof, err := q.GetTVDBEpisodeEvidence(t.Context(), p.UnitID)
	if err != nil || proof.TvdbEpisodeID != 9002 {
		t.Fatal("original source identity lost", err)
	}
}
func TestTVDBEvidenceSchema117TransactionRollbackPG(t *testing.T) {
	f := newCreationFixture(t, 117)
	p := seedTVDBEvidence(t, f)
	for _, fail := range []bool{true, false} {
		tx, err := f.pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		q := db.New(tx)
		if err := bindTVDBEvidence(t.Context(), q, p); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if _, err := q.InsertAuditLog(t.Context(), db.InsertAuditLogParams{ID: uuid.New(), ActorType: "user", ActorID: f.actor.ID, Action: "tv.metadata_refreshed", TargetType: "media", TargetID: p.MediaID.String(), Payload: []byte(`{}`)}); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if fail {
			if _, err := tx.Exec(t.Context(), `SELECT 1/0`); err == nil {
				t.Fatal("fault injection failed")
			}
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if tvdbEvidenceCounts(t, f) != ([6]int{}) {
				t.Fatal("tail rollback left proof or audit")
			}
		} else {
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if tvdbEvidenceCounts(t, f) != ([6]int{1, 1, 1, 0, 0, 0}) {
				t.Fatal("evidence and success ledger did not commit together")
			}
		}
	}
}
func TestTVDBEvidenceSchema117HistoryPG(t *testing.T) {
	f := newCreationFixture(t, 117)
	history := repositoryMigrationsThrough(t, 117)
	dsn := f.pool.Config().ConnConfig.ConnString()
	if v, err := postgres.Rollback(t.Context(), dsn, history); err != nil || v != 116 {
		t.Fatal("empty rollback failed", v, err)
	}
	if v, err := postgres.Migrate(t.Context(), dsn, history); err != nil || v != 117 {
		t.Fatal("reinstall failed", v, err)
	}
	p := seedTVDBEvidence(t, f)
	if err := bindTVDBEvidence(t.Context(), db.New(f.pool), p); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`UPDATE tvdb_episode_evidence SET tvdb_episode_id=9999`, `DELETE FROM tvdb_episode_evidence`, `UPDATE tvdb_series_identity SET tvdb_series_id=9999`, `DELETE FROM tvdb_series_identity`, `UPDATE media_unit SET retired=true WHERE id=$1`, `UPDATE media_unit SET episode_number=7 WHERE id=$1`} {
		var args []any
		if strings.Contains(statement, "$1") {
			args = []any{p.UnitID}
		}
		if _, err := f.pool.Exec(t.Context(), statement, args...); err == nil {
			t.Fatal("immutable source evidence changed", statement)
		}
	}
	if _, err := postgres.Rollback(t.Context(), dsn, history); err == nil {
		t.Fatal("source evidence discarded")
	}
	if tvdbEvidenceCounts(t, f) != ([6]int{1, 1, 0, 0, 0, 0}) {
		t.Fatal("failed rollback lost source evidence")
	}
	t.Logf("TVDB_SCHEMA117_HISTORY_PASS canonical_episode=%d source_episode=%d", p.TmdbEpisodeID, p.TvdbEpisodeID)
}
