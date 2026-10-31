package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTVEpisodeUpgradePreviewVideoPath(t *testing.T) {
	for _, p := range []string{"Show/S01E01.mkv", "Show/S01E01.MP4"} {
		if !tvEpisodeUpgradeVideoPath(p) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"", "../episode.mkv", "/episode.mkv", "Show/../episode.mkv", "Show\\episode.mkv", "episode.srt", "episode.mkv:stream"} {
		if tvEpisodeUpgradeVideoPath(p) {
			t.Fatal(p)
		}
	}
}

func TestTVEpisodeUpgradePreviewPG(t *testing.T) {
	ctx := t.Context()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, e := Migrate(ctx, dsn, "../../../migrations"); e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	actorID, work, root, unit, file, source, client, sub, candidate := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	actor := &ports.Actor{ID: actorID.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, e = q.CreateUser(ctx, db.CreateUserParams{ID: actorID, Username: "tv-preview-admin", PasswordHash: "unused", Role: "admin", IsActive: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: work, Title: "Breaking Bad", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	exec("UPDATE media SET year=2008 WHERE id=$1", work)
	exec("INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.tv','1396')", uuid.New(), work)
	if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: "/never-open-tv-preview", Name: "TV", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	exec("UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1", root)
	if _, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: work, UnitKey: "S01E01"}); e != nil {
		t.Fatal(e)
	}
	exec("UPDATE media_unit SET season_number=1,episode_number=1,air_date='2008-01-20' WHERE id=$1", unit)
	if _, e = q.BindTMDBEpisodeIdentity(ctx, db.BindTMDBEpisodeIdentityParams{UnitID: unit, ExternalID: 62085, TmdbSeriesID: 1396}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateIndexer(ctx, db.CreateIndexerParams{ID: source, Name: "No network", Url: "http://127.0.0.1:1", ApiKeyEncrypted: []byte("secret-api-cipher"), Categories: []byte("[]")}); e != nil {
		t.Fatal(e)
	}
	exec("UPDATE indexer SET health='online' WHERE id=$1", source)
	if _, e = q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "No network", ClientType: "qbittorrent", Url: "http://secret-endpoint.invalid", PasswordEncrypted: []byte("secret-password-cipher"), RemotePathMap: []byte("{}")}); e != nil {
		t.Fatal(e)
	}
	rules, _ := json.Marshal(quality.PolicyRules{CutoffScore: 1000, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300, quality.Res2160p: 400}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40}})
	if _, e = q.CreatePolicy(ctx, db.CreatePolicyParams{ID: "tv-preview", Name: "TV preview"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.InsertPolicyRevision(ctx, db.InsertPolicyRevisionParams{ID: uuid.New(), PolicyID: "tv-preview", Revision: 1, Rules: rules}); e != nil {
		t.Fatal(e)
	}
	sources, _ := json.Marshal([]uuid.UUID{source})
	if _, e = q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: sub, UserID: actorID, MediaID: work, RootID: root, QualityPolicyID: "tv-preview", QualityPolicyRevision: 1, IndexerIds: sources, ScopeType: "all"}); e != nil {
		t.Fatal(e)
	}
	exec("UPDATE subscription SET downloader_id=$2,upgrade_mode='stop_at_cutoff' WHERE id=$1", sub, client)
	exec("INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'Breaking Bad/S01E01.mkv',100,1)", file, root)
	if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file, UnitID: unit}); e != nil {
		t.Fatal(e)
	}
	old, _ := json.Marshal(media.Recognize("Breaking.Bad.2008.S01E01.1080p.Bluray.x264").Quality)
	if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: old}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.UpsertWantedItem(ctx, db.UpsertWantedItemParams{ID: uuid.New(), SubscriptionID: sub, UnitID: unit, IntentVersion: 1}); e != nil {
		t.Fatal(e)
	}
	exec("UPDATE wanted_item SET intent_version=(SELECT intent_version FROM subscription WHERE id=$1) WHERE subscription_id=$1", sub)
	title := "Breaking.Bad.2008.S01E01.2160p.Bluray.x265"
	profile, _ := json.Marshal(media.Recognize(title).Quality)
	if _, e = q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: source, Guid: "tv-upgrade-preview", Title: title, LinkEncrypted: []byte("secret-torrent-reference"), SizeBytes: 1000, QualityProfile: profile, SourceConfigVersion: 1}); e != nil {
		t.Fatal(e)
	}
	query := uuid.New()
	exec("INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,finished_at) VALUES($1,$2,'search','Breaking Bad',1,'succeeded',clock_timestamp())", query, source)
	exec("INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)", query, candidate)
	req := ports.TVEpisodeUpgradePreviewRequest{SubscriptionID: sub, UnitID: unit, CurrentFileID: file, CandidateID: candidate}
	read := func(tx pgx.Tx, a *ports.Actor) *tvEpisodeUpgradeMaterial {
		t.Helper()
		m, err := readTVEpisodeUpgradePreview(ctx, tx, a, req, nil)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	baseline := read(tx, actor)
	for _, table := range []string{"app_user", "policy", "subscription", "library_root", "media_unit", "media", "library_file", "library_file_unit", "unit_fact", "indexer", "download_client", "release_candidate"} {
		probe, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = probe.Exec(ctx, "SELECT 1 FROM "+table+" FOR UPDATE NOWAIT")
		var pgErr *pgconn.PgError
		locked := errors.As(err, &pgErr) && pgErr.Code == "55P03"
		upgradeRollback(ctx, probe)
		if !locked {
			t.Fatalf("preview did not lock %s: %v", table, err)
		}
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if baseline.Result.Reason != "" || !baseline.Result.Improvement || !baseline.Result.Comparable || baseline.Result.ExecutionAllowed || baseline.Result.Status != "preview_not_executable" || len(baseline.Result.Version) != 64 || baseline.ValidUntil.IsZero() || baseline.Target.Unit.EpisodeID != 62085 {
		t.Fatalf("preview: %+v", baseline.Result)
	}
	for _, secret := range []string{"secret-", "127.0.0.1", "password", "torrent-reference"} {
		if strings.Contains(string(baseline.Snapshot), secret) {
			t.Fatalf("snapshot leaked %q", secret)
		}
	}
	tests := []struct {
		name, sql            string
		changed, improvement bool
	}{
		{"counter", "UPDATE media_unit SET facts_version=facts_version+1", false, true},
		{"unit_timestamp", "UPDATE media_unit SET updated_at=clock_timestamp()", false, true},
		{"root_mutation_seq", "UPDATE library_root SET mutation_seq=mutation_seq+1", false, true},
		{"metadata", "UPDATE media_unit SET metadata_version=metadata_version+1", true, true},
		{"projection", "UPDATE wanted_item SET evaluated_facts_version=NULL,is_satisfied=false", false, true},
		{"root_generation", "UPDATE library_root SET generation=generation+1", true, false},
		{"client_endpoint", "UPDATE download_client SET username='changed'", true, true},
		{"source_config", "UPDATE indexer SET config_version=config_version+1", true, false},
		{"query_expired", "UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes'", true, false},
		{"unaired", "UPDATE media_unit SET air_date=CURRENT_DATE+1", true, false},
		{"unknown_air_date", "UPDATE media_unit SET air_date=NULL", true, false},
		{"scope", "UPDATE subscription SET scope_type='selected',selected_unit_ids='[]'", true, false},
		{"skip", "UPDATE wanted_item SET user_skipped=true", true, false},
		{"disabled", "UPDATE subscription SET upgrade_mode='disabled'", true, false},
		{"expired_fact", "UPDATE unit_fact SET expires_at=clock_timestamp()-interval '1 second'", true, false},
		{"extra_fact", "INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile) SELECT gen_random_uuid(),unit_id,stage,source_file_id,root_id,quality_profile FROM unit_fact", true, false},
		{"shared_file", "INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) SELECT gen_random_uuid(),media_id,'S01E02',1,2 FROM media_unit; INSERT INTO library_file_unit(file_id,unit_id) SELECT f.id,u.id FROM library_file f CROSS JOIN media_unit u WHERE u.unit_key='S01E02'", true, false},
		{"same_tier", "UPDATE release_candidate SET title='Breaking.Bad.2008.S01E01.1080p.Bluray.x264',quality_profile=(SELECT quality_profile FROM unit_fact LIMIT 1)", true, false},
		{"different_episode", "UPDATE release_candidate SET title='Breaking.Bad.2008.S01E02.2160p.Bluray.x265'", true, false},
		{"forged_quality", "UPDATE release_candidate SET quality_profile=(SELECT quality_profile FROM unit_fact LIMIT 1)", true, false},
		{"second_file", "INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) SELECT gen_random_uuid(),root_id,'other.mkv',100,1 FROM library_file; INSERT INTO library_file_unit(file_id,unit_id) SELECT f.id,u.id FROM library_file f CROSS JOIN media_unit u WHERE f.rel_path='other.mkv'", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer upgradeRollback(context.Background(), tx)
			if _, err = tx.Exec(ctx, tt.sql); err != nil {
				t.Fatal(err)
			}
			m := read(tx, actor)
			if (m.Result.Version != baseline.Result.Version) != tt.changed || m.Result.Improvement != tt.improvement {
				t.Fatalf("changed=%t preview=%+v", m.Result.Version != baseline.Result.Version, m.Result)
			}
		})
	}
	t.Run("reservation_side_effects", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer upgradeRollback(ctx, tx)
		tq := db.New(tx)
		op := uuid.New()
		if _, err = tq.CreateAcquisitionLane(ctx, db.CreateAcquisitionLaneParams{ID: uuid.New(), UnitID: unit, OperationID: op, CandidateID: candidate, SourceRootID: root, SourceGeneration: baseline.Root.Generation, SourceStamp: baseline.Result.Version}); err != nil {
			t.Fatal(err)
		}
		if _, err = tq.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: uuid.New(), UnitID: unit, RootID: root, Purpose: "upgrade", CandidateID: candidate, OperationID: op}); err != nil {
			t.Fatal(err)
		}
		if _, err = tq.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: uuid.New(), OperationID: op, UnitID: unit, RootID: root, ActorType: "user", ActorID: actor.ID, SubmissionFence: []byte(`{"goal":"tv_episode_upgrade"}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, "UPDATE wanted_item SET evaluated_facts_version=NULL,evaluated_write_token=NULL,is_satisfied=false,last_evaluated_at=clock_timestamp(),updated_at=clock_timestamp(); UPDATE media_unit SET updated_at=clock_timestamp()"); err != nil {
			t.Fatal(err)
		}
		current := read(tx, actor)
		if current.Unit.FactsVersion <= baseline.Unit.FactsVersion || current.Result.Version != baseline.Result.Version || string(current.Snapshot) != string(baseline.Snapshot) || !current.Result.Improvement || current.Result.Reason != "" {
			t.Fatalf("reservation changed protected evidence: %+v", current.Result)
		}
	})
	t.Run("database_permission", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer upgradeRollback(ctx, tx)
		if _, err = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", actorID); err != nil {
			t.Fatal(err)
		}
		if _, err = readTVEpisodeUpgradePreview(ctx, tx, actor, req, nil); !errors.Is(err, ports.ErrForbidden) {
			t.Fatalf("revoked actor: %v", err)
		}
	})
}
