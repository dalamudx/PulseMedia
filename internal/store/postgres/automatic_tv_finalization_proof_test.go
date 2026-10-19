package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"reflect"
)

func TestAutomaticTVFinalizationQueryMembershipPG(t *testing.T) {
	for _, kind := range []string{"legacy-query", "other-page", "global-refresh", "stale-parser", "wrong-episode"} {
		t.Run(kind, func(t *testing.T) {
			f := newTVExecutionFixture(t)
			r := f.start(t)
			original := tvExecutionCandidate(t, f, "original", "720p", 1)
			if kind == "stale-parser" {
				var profile map[string]any
				if e := json.Unmarshal(original.QualityProfile, &profile); e != nil {
					t.Fatal(e)
				}
				profile["source_parser_version"] = "obsolete"
				original.QualityProfile, _ = json.Marshal(profile)
			}
			if kind == "wrong-episode" {
				original.Title = "Trusted.Native.Series.2024.S02E04.720p.WEB-DL.H264"
			}
			tvExecutionPage(t, f, r, original)
			if kind == "legacy-query" || kind == "other-page" || kind == "global-refresh" {
				c := tvExecutionCandidate(t, f, "unassociated", "2160p", 1000)
				if kind == "global-refresh" {
					c.ID = original.ID
					c.Guid = original.Guid
				}
				spec := ports.SearchQuerySpec{ID: uuid.New(), IndexerID: f.indexer, ConfigVersion: 1, Keyword: "legacy global observation"}
				if kind == "other-page" {
					spec.Page = 1
				}
				p := f.permit(t, spec)
				if _, e := p.CommitQuery(t.Context(), automaticTVCommit(spec, c)); e != nil {
					t.Fatal("global observation", e)
				}
				p.Close()
			}
			lease := tvExecutionLease(t, f, r)
			d, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "legacy-query" || kind == "other-page" || kind == "global-refresh" {
				// Shared upserts retain original source facts, so a refresh may update
				// seeders but cannot lend the newer query's 2160p quality.
				if kind == "global-refresh" && d.BestScore != 230 {
					t.Fatalf("borrowed new quality %+v", d)
				}
				if d.State != "reserved" || d.SelectedCandidateID == nil || *d.SelectedCandidateID != original.ID {
					t.Fatalf("borrowed foreign observation %+v", d)
				}
			} else if d.State != "closed" || d.OperationID != uuid.Nil {
				t.Fatalf("stale/invalid facts selected %+v", d)
			}
		})
	}
}

func TestAutomaticTVFinalizationDeadlinePreservesPhysicalSourcePG(t *testing.T) {
	f := newTVExecutionFixture(t)
	r, e := f.repo.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: f.wanted, Window: 1500 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	w := f.load(t, r)
	spec := automaticTVSpec(t, w, 0)
	p := f.permit(t, spec)
	candidate := tvExecutionCandidate(t, f, "fast-page", "1080p", 1)
	if _, e = p.CommitQuery(t.Context(), automaticTVCommit(spec, candidate)); e != nil {
		t.Fatal(e)
	}
	// Keep the physical permit owned, as if a subsequent source I/O still ran.
	lease := tvExecutionLease(t, f, r)
	repo := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode)
	before := tvExecutionState(t, f)
	if _, e = repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease); !errors.Is(e, ports.ErrAcquisitionRoundNotReady) {
		t.Fatal("early finalization", e)
	}
	if before != tvExecutionState(t, f) {
		t.Fatal("pending source mutated round")
	}
	if _, e = f.pool.Exec(t.Context(), `SELECT pg_sleep(GREATEST(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.01)`, r.Deadline); e != nil {
		t.Fatal(e)
	}
	d, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
	if e != nil || d.State != "reserved" || d.SelectedCandidateID == nil || *d.SelectedCandidateID != candidate.ID {
		t.Fatal("deadline lost fast proof", d, e)
	}
	release, e := postgres.AcquireIndexerPermit(t.Context(), f.pool, f.indexer)
	if release != nil {
		release()
	}
	if !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("finalizer released physical I/O slot", e)
	}
	if e = f.repo.FinishAutomaticTVSource(t.Context(), *w, ports.AutomaticTVSourceSucceeded); !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
		t.Fatal("late source write accepted", e)
	}
	p.Close()
}

func TestAutomaticTVFinalizationProtectionHoldsPG(t *testing.T) {
	for _, kind := range []string{"unknown-target-file", "other-root-reuse", "unit-reservation", "default-lane", "pending-demand"} {
		t.Run(kind, func(t *testing.T) {
			f := newTVExecutionFixture(t)
			r := f.start(t)
			candidate := tvExecutionCandidate(t, f, "protected", "1080p", 1)
			tvExecutionPage(t, f, r, candidate)
			lease := tvExecutionLease(t, f, r)
			switch kind {
			case "unknown-target-file", "other-root-reuse":
				root := f.tv
				if kind == "other-root-reuse" {
					root = f.movie
				}
				file := uuid.New()
				execIntent(t, f.pool, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'unknown-existing.mkv',1234,1)`, file, root)
				execIntent(t, f.pool, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, file, f.unit)
			case "unit-reservation":
				execIntent(t, f.pool, `INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state,expires_at) VALUES($1,$2,$3,'acquire',$4,$5,'reserved',clock_timestamp()-interval '1 day')`, uuid.New(), f.unit, f.tv, candidate.ID, uuid.New())
			case "default-lane":
				execIntent(t, f.pool, `INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,source_root_id,source_generation,source_stamp) VALUES($1,$2,$3,$4,$5,$6,'old-protected-source')`, uuid.New(), f.unit, uuid.New(), candidate.ID, f.tv, r.Snapshot.RootGeneration)
			case "pending-demand":
				execIntent(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id) VALUES($1,$2,$3,$4,'system','original-protection')`, uuid.New(), uuid.New(), f.unit, f.tv)
			}
			before := tvExecutionState(t, f)
			if _, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, lease); e == nil {
				t.Fatal("existing protection ignored")
			}
			if before != tvExecutionState(t, f) {
				t.Fatal("original protection was changed")
			}
			if intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_execution_authority`) != 0 {
				t.Fatal("protection manufactured execution authority")
			}
		})
	}
}

func TestAutomaticTVFinalizationIgnoresCachedProjectionPG(t *testing.T) {
	f := newTVExecutionFixture(t)
	r := f.start(t)
	tvExecutionPage(t, f, r, tvExecutionCandidate(t, f, "cached", "1080p", 1))
	lease := tvExecutionLease(t, f, r)
	execIntent(t, f.pool, `UPDATE wanted_item SET is_satisfied=true,current_fact_stage='imported' WHERE id=$1`, f.wanted)
	d, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
	if e != nil || d.State != "reserved" {
		t.Fatal("cached satisfaction overrode current facts", d, e)
	}
}

func TestAutomaticTVFinalizationHistoricalPolicyChangePG(t *testing.T) {
	f := newTVExecutionFixture(t)
	r := f.start(t)
	tvExecutionPage(t, f, r, tvExecutionCandidate(t, f, "history-policy", "1080p", 1))
	lease := tvExecutionLease(t, f, r)
	repo := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode)
	original, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
	if e != nil {
		t.Fatal(e)
	}
	q := db.New(f.pool)
	p, e := q.CreatePolicy(t.Context(), db.CreatePolicyParams{ID: "new-current-policy", Name: "new policy"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = q.InsertPolicyRevision(t.Context(), db.InsertPolicyRevisionParams{ID: uuid.New(), PolicyID: p.ID, Revision: 1, Rules: r.Snapshot.Rules}); e != nil {
		t.Fatal(e)
	}
	execIntent(t, f.pool, `UPDATE subscription SET quality_policy_id=$2,quality_policy_revision=1,intent_version=intent_version+1,state='paused' WHERE id=$1`, f.sub, p.ID)
	var frozenAuthority []byte
	if e = f.pool.QueryRow(t.Context(), `SELECT snapshot FROM automatic_tv_execution_authority WHERE operation_id=$1`, original.OperationID).Scan(&frozenAuthority); e != nil {
		t.Fatal(e)
	}
	execIntent(t, f.pool, `UPDATE library_root SET path=path||'-changed' WHERE id=$1`, f.tv)
	var retainedAuthority []byte
	if e = f.pool.QueryRow(t.Context(), `SELECT snapshot FROM automatic_tv_execution_authority WHERE operation_id=$1`, original.OperationID).Scan(&retainedAuthority); e != nil {
		t.Fatal(e)
	}
	if string(frozenAuthority) != string(retainedAuthority) {
		t.Fatal("path change rewrote historical authority")
	}
	before := tvExecutionState(t, f)
	again, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
	if e != nil || !reflect.DeepEqual(original, again) || before != tvExecutionState(t, f) {
		t.Fatal("historical recovery re-executed current policy", e)
	}
}

func TestAutomaticTVFinalizationFinishDelayExpiresLeasePG(t *testing.T) {
	f := newTVExecutionFixture(t)
	r := f.start(t)
	tvExecutionPage(t, f, r, tvExecutionCandidate(t, f, "delay", "1080p", 1))
	lease := tvExecutionLease(t, f, r)
	execIntent(t, f.pool, `CREATE FUNCTION tv_finish_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='automatic_tv_finalize' AND NEW.state='succeeded' THEN PERFORM pg_sleep(0.7); END IF; RETURN NEW; END $$; CREATE TRIGGER tv_finish_delay BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION tv_finish_delay()`)
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()+interval '0.5 seconds' WHERE id=$1`, lease.ID)
	before := tvExecutionState(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(ctx, r.ID, lease); !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
		t.Fatal("late finish accepted", e)
	}
	if before != tvExecutionState(t, f) {
		t.Fatal("late finish committed preallocation")
	}
}
