package search_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	search "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"sync"
	"testing"
	"time"
)

func TestRoundMetadataRevisionsRejectOldEvidence(t *testing.T) {
	for _, kind := range []string{"unit", "identity"} {
		t.Run(kind, func(t *testing.T) {
			f := runtimeFixture(t)
			round := f.collect(t)
			job := f.finalJob(t, round)
			ctx := context.Background()
			wanted, err := f.q.GetWantedItemByID(ctx, f.wanted)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "unit" {
				_, err = f.pool.Exec(ctx, "UPDATE media_unit SET air_date=CURRENT_DATE+1 WHERE id=$1", wanted.UnitID)
			} else {
				unit, e := f.q.GetSearchUnit(ctx, wanted.UnitID)
				if e != nil {
					t.Fatal(e)
				}
				_, err = f.q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: unit.MediaID, Namespace: "tmdb.movie", ExternalID: "930001"})
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
			if !errors.Is(err, search.ErrRoundAborted) || result.State != "aborted" {
				t.Fatal("metadata changed but old candidate still authorized", result, err)
			}
			var count int
			if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&count); err != nil || count != 0 {
				t.Fatal("stale metadata acquired", count, err)
			}
		})
	}
}

func TestFinalizeUsesSharedOwnership(t *testing.T) {
	for _, test := range []struct{ name, profile, state, reason string }{{"verified", `{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`, "completed", "already_owned"}, {"unknown", `{}`, "expired", "ownership_unknown"}, {"unverified", "", "expired", "ownership_unknown"}} {
		t.Run(test.name, func(t *testing.T) {
			f := runtimeFixture(t)
			round := f.collect(t)
			job := f.finalJob(t, round)
			ctx := context.Background()
			wanted, err := f.q.GetWantedItemByID(ctx, f.wanted)
			if err != nil {
				t.Fatal(err)
			}
			file := uuid.New()
			if _, err = f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: file, RootID: f.root, RelPath: "late-observation.mkv", SizeBytes: 100, MtimeNs: 1}); err != nil {
				t.Fatal(err)
			}
			if err = f.q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file, UnitID: wanted.UnitID}); err != nil {
				t.Fatal(err)
			}
			if test.profile != "" {
				if _, err = f.q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: wanted.UnitID, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, RootID: pgtype.UUID{Bytes: f.root, Valid: true}, QualityProfile: []byte(test.profile)}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
			if err != nil || result.State != test.state || result.Reason != test.reason {
				t.Fatal("finalizer disagreed with current owned-evidence contract", result, err)
			}
			var submissions int
			if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&submissions); err != nil || submissions != 0 {
				t.Fatal("late owned/unknown evidence created duplicate acquisition", submissions, err)
			}
			persisted, err := f.q.GetJobByID(ctx, job.ID)
			if err != nil || persisted.State != "succeeded" {
				t.Fatal("hold did not atomically acknowledge finalization", persisted, err)
			}
		})
	}
}

func TestFinalizeDoesNotHoldWantedWhileWaitingRoot(t *testing.T) {
	f := runtimeFixture(t)
	round := f.collect(t)
	job := f.finalJob(t, round)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	importer, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = importer.Rollback(ctx) }()
	if _, err = importer.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", f.root); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var worker sync.WaitGroup
	worker.Add(1)
	defer func() { cancel(); _ = importer.Rollback(context.Background()); worker.Wait() }()
	go func() {
		defer worker.Done()
		_, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockAcquisitionRoot%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("finalizer did not wait for the root")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A root-owning importer must still be able to update wanted. With the old
	// wanted->root finalizer this creates a real lock cycle (not just a slow query).
	updateCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if _, err = importer.Exec(updateCtx, "UPDATE wanted_item SET is_skipped=true WHERE id=$1", f.wanted); err != nil {
		t.Fatal("finalizer locked wanted before acquiring root", err)
	}
	if err = importer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, search.ErrRoundAborted) {
		t.Fatal("late skipped target acquired", err)
	}
	persisted, err := f.q.GetSearchRoundByID(ctx, round.ID)
	if err != nil || persisted.AbortReason.String != "wanted_changed" {
		t.Fatal("finalize did not re-read wanted under root/unit locks", persisted, err)
	}
	var submissions int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&submissions); err != nil || submissions != 0 {
		t.Fatal("aborted finalizer submitted download", submissions, err)
	}
}
