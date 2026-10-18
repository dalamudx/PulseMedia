package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestTargetDatabaseGuardRejectsLegacyCompletion(t *testing.T) {
	f := safetySetup(t)
	ctx := context.Background()
	_, step := f.plan(t)
	if _, e := f.pool.Exec(ctx, "UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1", f.unit); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, "UPDATE transfer_step SET state='completed' WHERE id=$1", step.ID); e == nil {
		t.Fatal("old binary completion promoted stale evidence")
	}
	fresh, e := f.q.ListStepsForPlan(ctx, step.PlanID)
	if e != nil || len(fresh) != 1 || fresh[0].State == "completed" {
		t.Fatal("stale completion changed state", fresh, e)
	}
}

func TestFrozenTargetEvidenceCannotBeResignedOrDropped(t *testing.T) {
	f := safetySetup(t)
	ctx := context.Background()
	_, step := f.plan(t)
	for _, sql := range []string{"UPDATE transfer_step SET target_evidence=NULL WHERE id=$1", "UPDATE transfer_step SET target_evidence=jsonb_set(target_evidence,'{unit_version}','999') WHERE id=$1", "UPDATE transfer_step SET unit_id=gen_random_uuid() WHERE id=$1"} {
		if _, e := f.pool.Exec(ctx, sql, step.ID); e == nil {
			t.Fatal("frozen target rewritten")
		}
	}
}

func TestFrozenTargetVersionsRejectChangedCanonical(t *testing.T) {
	for _, kind := range []string{"unit", "media"} {
		for _, phase := range []string{"before_write", "after_link"} {
			t.Run(kind+"_"+phase, func(t *testing.T) {
				f := safetySetup(t)
				ctx := context.Background()
				p, step := f.plan(t)
				var frozen transferTargetEvidence
				if json.Unmarshal(step.TargetEvidence, &frozen) != nil || frozen.UnitID != f.unit || frozen.UnitVersion < 1 || frozen.MediaVersion < 1 {
					t.Fatal("not frozen", string(step.TargetEvidence))
				}
				target := filepath.Join(f.target, step.TargetRel.String)
				if phase == "after_link" {
					if _, e := f.pool.Exec(ctx, "UPDATE transfer_plan SET state='executing',execution_owner='version-test',execution_token=execution_token+1,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1", p.ID); e != nil {
						t.Fatal(e)
					}
					var e error
					p, e = f.q.GetTransferPlanByID(ctx, p.ID)
					if e != nil {
						t.Fatal(e)
					}
					if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
						t.Fatal(e)
					}
					if e = os.Link(f.sourcePath, target); e != nil {
						t.Fatal(e)
					}
				}
				query := "UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1"
				if kind == "media" {
					query = "UPDATE media SET metadata_version=metadata_version+1 WHERE id=(SELECT media_id FROM media_unit WHERE id=$1)"
				}
				if _, e := f.pool.Exec(ctx, query, f.unit); e != nil {
					t.Fatal(e)
				}
				var e error
				if phase == "after_link" {
					e = f.svc.commitStep(ctx, p, step, f.observationTime(t))
				} else {
					e = f.svc.ExecutePlan(ctx, p.ID)
				}
				if !errors.Is(e, ErrReviewRequired) {
					t.Fatal("old version executed", e)
				}
				var facts int
				if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE unit_id=$1", f.unit).Scan(&facts); e != nil || facts != 0 {
					t.Fatal("stale target facts", facts, e)
				}
				_, e = os.Stat(target)
				if phase == "before_write" && !os.IsNotExist(e) {
					t.Fatal("published stale target", e)
				}
				if phase == "after_link" && e != nil {
					t.Fatal("deleted unknown physical link", e)
				}
			})
		}
	}
}

func TestMovieIngestionRejectsLateTVIdentity(t *testing.T) {
	f := safetySetup(t)
	ctx := context.Background()
	p, step := f.plan(t)
	if _, e := f.pool.Exec(ctx, "UPDATE transfer_plan SET state='executing',execution_owner='late-kind-test',execution_token=execution_token+1,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1", p.ID); e != nil {
		t.Fatal(e)
	}
	claimed, e := f.q.GetTransferPlanByID(ctx, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(f.target, step.TargetRel.String)
	if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.Link(f.sourcePath, target); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE media SET media_type='tv' WHERE id=(SELECT media_id FROM media_unit WHERE id=$1)", f.unit); e != nil {
		t.Fatal(e)
	}
	if e = f.svc.commitStep(ctx, claimed, step, f.observationTime(t)); !errors.Is(e, ErrReviewRequired) {
		t.Fatal("late identity committed facts", e)
	}
	var facts int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE unit_id=$1", f.unit).Scan(&facts); e != nil || facts != 0 {
		t.Fatal("late TV imported", facts, e)
	}
	if _, e = os.Stat(target); e != nil {
		t.Fatal("unknown physical file deleted", e)
	}
}

func TestApprovedMoviePlanRejectsLateTVIdentity(t *testing.T) {
	f := safetySetup(t)
	ctx := context.Background()
	p, step := f.plan(t)
	if _, e := f.pool.Exec(ctx, "UPDATE media SET media_type='tv' WHERE id=(SELECT media_id FROM media_unit WHERE id=$1)", f.unit); e != nil {
		t.Fatal(e)
	}
	if e := f.svc.ExecutePlan(ctx, p.ID); !errors.Is(e, ErrReviewRequired) {
		t.Fatal("old movie plan accepted changed parent", e)
	}
	if _, e := os.Stat(filepath.Join(f.target, step.TargetRel.String)); !os.IsNotExist(e) {
		t.Fatal("physical publish before kind guard", e)
	}
	var facts int
	if e := f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE unit_id=$1 ", f.unit).Scan(&facts); e != nil || facts != 0 {
		t.Fatal("old identity imported", facts, e)
	}
}

func TestMoviePlanRejectsTVTarget(t *testing.T) {
	f := safetySetup(t)
	ctx := context.Background()
	if _, e := f.pool.Exec(ctx, "UPDATE media_unit SET unit_key='S01E01',season_number=1,episode_number=1 WHERE id=$1", f.unit); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, "UPDATE media SET media_type='tv' WHERE id=(SELECT media_id FROM media_unit WHERE id=$1)", f.unit); e != nil {
		t.Fatal(e)
	}
	p, _, e := f.svc.CreateMoviePlan(ctx, uuid.New(), f.root, f.unit, f.sourcePath, "Movie", 2020, quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray})
	if !errors.Is(e, ErrReviewRequired) || p != nil {
		t.Fatalf("movie plan accepted TV target without coverage freeze: plan=%v err=%v", p, e)
	}
	var count int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM transfer_plan").Scan(&count); e != nil || count != 0 {
		t.Fatal("partial unsafe plan", count, e)
	}
}
func TestMoviePlanRejectsRetiredTarget(t *testing.T) {
	f := safetySetup(t)
	ctx := context.Background()
	if _, e := f.pool.Exec(ctx, "UPDATE media_unit SET retired=true WHERE id=$1", f.unit); e != nil {
		t.Fatal(e)
	}
	p, _, e := f.svc.CreateMoviePlan(ctx, uuid.New(), f.root, f.unit, f.sourcePath, "Movie", 2020, quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray})
	if !errors.Is(e, ErrReviewRequired) || p != nil {
		t.Fatalf("movie plan accepted retired target: plan=%v err=%v", p, e)
	}
}
