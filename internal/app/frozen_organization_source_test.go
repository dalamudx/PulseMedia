package app

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"testing"
)

func TestFrozenOrganizationInputRejectsChangedSourceAndMetadata(t *testing.T) {
	for _, change := range []string{"valid-automatic-null-match", "source-title", "root-generation", "unit-metadata", "media-metadata", "frozen-calendar", "frozen-compact-date", "frozen-partial-movie", "frozen-collection", "frozen-text-calendar", "frozen-numbered-collection", "frozen-long-numbered-collection", "frozen-numeric-group-year", "frozen-french-collection", "frozen-rip-tail-year", "frozen-season-scope"} {
		t.Run(change, func(t *testing.T) {
			f := organizationSetup(t)
			ctx := context.Background()
			task, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
			if e != nil {
				t.Fatal(e)
			}
			candidate, e := f.q.GetCandidateByID(ctx, task.CandidateID)
			if e != nil {
				t.Fatal(e)
			}
			root, e := f.q.GetLibraryRootByID(ctx, f.root)
			if e != nil {
				t.Fatal(e)
			}
			unit, e := f.q.GetSearchUnit(ctx, f.unit)
			if e != nil {
				t.Fatal(e)
			}
			m, e := f.q.GetMediaByID(ctx, f.media)
			if e != nil {
				t.Fatal(e)
			}
			// Simulate an unchanged historic acquisition whose parser once certified
			// date-only or partial movie coverage. The original source stamp matches;
			// only re-evaluating current semantics can prevent a new plan/signature.
			legacy := map[string]string{
				"frozen-season-scope":             "Example.Movie.2024.S01.1080p.BluRay.x264",
				"frozen-rip-tail-year":            "Example.Movie.DVDRip.2024.1080p.x264",
				"frozen-french-collection":        "Example.Movie.2024.INTEGRALE.MULTI.1080p.BluRay.x264",
				"frozen-numeric-group-year":       "Example.Movie.1080p.BluRay.x264-1920",
				"frozen-long-numbered-collection": "Example.Movie.Heptalogy.SWESUB.2024.1080p.BluRay.x264",
				"frozen-numbered-collection":      "Example.Movie.Quadrilogy.2024.1080p.BluRay.x264",
				"frozen-text-calendar":            "Example.Movie.3rd.Nov.2024.1080p.BluRay.x264",
				"frozen-calendar":                 "Example.Movie.2024.01.02.1080p.BluRay.x264",
				"frozen-compact-date":             "Example.Movie.20240102.1080p.BluRay.x264",
				"frozen-partial-movie":            "Example.Movie.2024.1080p.BluRay.sample.x264",
				"frozen-collection":               "Example.Movie.Complete.Collection.2024.1080p.BluRay.x264",
			}
			if raw, ok := legacy[change]; ok {
				if _, e = f.pool.Exec(ctx, "UPDATE release_candidate SET title=$2,matched_unit_id=NULL WHERE id=$1", candidate.ID, raw); e != nil {
					t.Fatal(e)
				}
				candidate.Title = raw
			}
			stamp := quality.AcquisitionSourceStamp(candidate.Title, candidate.InfoHash.String, candidate.LinkEncrypted, candidate.QualityProfile)
			if _, e = f.q.CreateAcquisitionLane(ctx, db.CreateAcquisitionLaneParams{ID: uuid.New(), OperationID: f.op, UnitID: f.unit, CandidateID: candidate.ID, SourceRootID: f.root, SourceGeneration: root.Generation, SourceStamp: stamp}); e != nil {
				t.Fatal(e)
			}
			if e = f.q.BindPhysicalAcquisitionTask(ctx, db.BindPhysicalAcquisitionTaskParams{OperationID: f.op, TaskID: pgtype.UUID{Bytes: task.ID, Valid: true}}); e != nil {
				t.Fatal(e)
			}
			fence, _ := json.Marshal(ports.DownloadDemandFence{CandidateID: candidate.ID, UnitID: f.unit, MediaID: f.media, RootID: f.root, RootGeneration: root.Generation, UnitMetadataVersion: unit.MetadataVersion, MediaMetadataVersion: m.MetadataVersion, SourceStamp: stamp})
			if _, e = f.pool.Exec(ctx, "UPDATE download_requirement SET submission_fence=$2 WHERE operation_id=$1", f.op, fence); e != nil {
				t.Fatal(e)
			}
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, e = f.pool.Exec(ctx, sql, args...); e != nil {
					t.Fatal(e)
				}
			}
			switch change {
			case "valid-automatic-null-match":
				exec("UPDATE release_candidate SET matched_unit_id=NULL WHERE id=$1", candidate.ID)
			case "source-title":
				exec("UPDATE release_candidate SET title='Changed.2020.1080p.BluRay' WHERE id=$1", candidate.ID)
			case "root-generation":
				exec("UPDATE library_root SET generation=generation+1,baseline_completed=true WHERE id=$1", f.root)
			case "unit-metadata":
				exec("UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1", f.unit)
			case "media-metadata":
				exec("UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1", f.media)
			}
			f.observe(t)
			if e = f.newService().OrganizeDownload(ctx, f.claim(t, "frozen-proof")); e != nil {
				t.Fatal(e)
			}
			after, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
			if e != nil {
				t.Fatal(e)
			}
			if change == "valid-automatic-null-match" {
				if after.OrganizationState != "completed" || f.count(t, "library_file_unit") != 1 {
					t.Fatal("frozen approved coverage ignored", after)
				}
			} else {
				if after.OrganizationState != "review_required" || f.count(t, "library_file_unit") != 0 || f.count(t, "transfer_plan") != 0 || f.count(t, "unit_fact") != 0 {
					t.Fatal("old frozen source copied to changed metadata/root", change, after)
				}
				var protected int
				if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL", f.op).Scan(&protected); e != nil || protected != 1 {
					t.Fatal("stale physical lane released", protected, e)
				}
			}
		})
	}
}
