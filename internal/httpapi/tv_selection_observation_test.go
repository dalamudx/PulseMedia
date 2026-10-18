package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

func selectionFixture(t *testing.T) (*completeTVFixture, *acquisition.TVSelectionService, *postgres.TVSelectionStore, ports.TVSelectionRequest) {
	t.Helper()
	f := newCompleteTVFixture(t)
	repo := postgres.NewTVSelectionStore(postgres.NewEpisodeMappingStore(postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(f.f.a.DBPool(), idgen.New())), idgen.New()))
	r := ports.TVSelectionRequest{SubscriptionID: f.f.sub.Id, Season: 1, PackPolicy: quality.PackPolicy{Mode: quality.PackModePreferPack, MinMissingRatio: 1}, Packs: []ports.TVSelectionCandidateRef{{CandidateID: f.candidate, ReviewID: f.command.ReviewID}}}
	selectionProject(t, f)
	return f, acquisition.NewTVSelectionService(repo), repo, r
}
func selectionProject(t *testing.T, f *completeTVFixture) {
	t.Helper()
	ctx := context.Background()
	sub, e := f.f.a.Queries().GetSubscriptionByID(ctx, f.f.sub.Id)
	if e != nil {
		t.Fatal(e)
	}
	wanted, e := f.f.a.Queries().ListWantedItemsForSubscription(ctx, sub.ID)
	if e != nil {
		t.Fatal(e)
	}
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(f.f.a.DBPool()))
	for _, w := range wanted {
		if _, e = evaluator.EvaluateWantedItem(ctx, w.ID, quality.Policy{ID: sub.QualityPolicyID, Revision: sub.QualityPolicyRevision}); e != nil {
			t.Fatal(e)
		}
	}
}
func selectionState(t *testing.T, f *completeTVFixture) string {
	t.Helper()
	out := ""
	// Compare actual rows, including projections, not merely newly queued job counts.
	for _, table := range []string{"wanted_item", "job", "download_task", "unit_reservation", "acquisition_reservation", "download_requirement", "complete_tv_execution_authority", "episode_execution_authority", "library_file", "unit_fact", "outbox_event", "audit_log", "idempotency_record", "transfer_plan", "tv_coverage_candidate_review", "episode_candidate_review"} {
		var v string
		if e := f.f.a.DBPool().QueryRow(context.Background(), "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "+table+" t").Scan(&v); e != nil {
			t.Fatal(e)
		}
		out += table + v
	}
	return out
}
func selectionAssert(t *testing.T, p *acquisition.TVSelectionPreview, e error, kind, reason string) {
	t.Helper()
	if e != nil || p == nil {
		t.Fatalf("preview error: %v", e)
	}
	if p.Status != "preview_not_executable" || p.ExecutionAllowed || p.Decision.Kind != kind || p.Decision.Reason != reason {
		t.Fatalf("unexpected preview %+v", p)
	}
}
func TestTVSelectionObservationPGService(t *testing.T) {
	f, svc, repo, r := selectionFixture(t)
	ctx := context.Background()
	before := selectionState(t, f)
	v, e := svc.Preview(ctx, f.actor, r)
	selectionAssert(t, v, e, "pack", "only_eligible_alternative")
	if len(v.Decision.CandidateIDs) != 1 || v.Decision.CandidateIDs[0] != f.candidate.String() {
		t.Fatal(v)
	}
	observed, e := repo.ReadTVSelection(ctx, f.actor, r)
	if e != nil {
		t.Fatal(e)
	}
	if len(observed.Current.Canonical) != 2 || len(observed.Wanted) != 2 || len(observed.Season.Units) != 2 || !observed.Current.AccountAuthorized || !observed.Current.ObjectAuthorized {
		t.Fatal("incomplete observation")
	}
	r.Expected = &observed.Current.Version
	v, e = svc.Preview(ctx, f.actor, r)
	selectionAssert(t, v, e, "pack", "only_eligible_alternative")
	r.Packs = nil
	r.Expected = nil
	r.PackPolicy.Mode = quality.PackModeRequirePack
	v, e = svc.Preview(ctx, f.actor, r)
	selectionAssert(t, v, e, "hold", "required_pack_unavailable")
	if before != selectionState(t, f) {
		t.Fatal("preview wrote persistent state")
	}
}
func TestTVSelectionObservationPGCurrentness(t *testing.T) {
	for _, name := range []string{"absent-wanted", "stale-projection", "expired-projection", "unknown-ownership", "source-expired", "source-config", "client-config", "review-size", "unit-version", "provider", "metadata", "root", "mapping"} {
		t.Run(name, func(t *testing.T) {
			f, svc, _, r := selectionFixture(t)
			switch name {
			case "absent-wanted":
				f.exec(t, "DELETE FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2", f.f.sub.Id, f.f.units[0])
			case "stale-projection":
				f.exec(t, "UPDATE media_unit SET facts_version=facts_version+1 WHERE id=$1", f.f.units[0])
			case "expired-projection":
				f.exec(t, "UPDATE wanted_item SET projection_valid_until=clock_timestamp()-interval '1 second' WHERE subscription_id=$1", f.f.sub.Id)
			case "unknown-ownership":
				f.exec(t, "INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) SELECT $1,root_id,'unknown.mkv',1024,1 FROM subscription WHERE id=$2", uuid.New(), f.f.sub.Id)
				f.exec(t, "INSERT INTO library_file_unit(file_id,unit_id) SELECT id,$1 FROM library_file WHERE rel_path='unknown.mkv'", f.f.units[0])
				selectionProject(t, f)
			case "source-expired":
				f.exec(t, "UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes' WHERE indexer_id=$1", f.indexer)
			case "source-config":
				f.exec(t, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", f.indexer)
			case "client-config":
				f.exec(t, "UPDATE download_client SET url='http://changed.invalid',updated_at=clock_timestamp() WHERE id=(SELECT downloader_id FROM subscription WHERE id=$1)", f.f.sub.Id)
			case "review-size":
				f.exec(t, "UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1", f.candidate)
			case "unit-version":
				f.exec(t, "UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1", f.f.units[0])
			case "provider":
				f.exec(t, "UPDATE media_identity SET external_id='101' WHERE namespace='tmdb.tv' AND media_id=(SELECT media_id FROM subscription WHERE id=$1)", f.f.sub.Id)
			case "metadata":
				f.exec(t, "UPDATE media SET metadata_version=metadata_version+1 WHERE id=(SELECT media_id FROM subscription WHERE id=$1)", f.f.sub.Id)
			case "root":
				f.exec(t, "UPDATE library_root SET generation=generation+1 WHERE id=(SELECT root_id FROM subscription WHERE id=$1)", f.f.sub.Id)
			case "mapping":
				f.exec(t, "UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1", f.f.sub.Id)
			}
			before := selectionState(t, f)
			r.PackPolicy.Mode = quality.PackModeRequirePack
			p, e := svc.Preview(context.Background(), f.actor, r)
			if e == nil {
				selectionAssert(t, p, e, "hold", "required_pack_unavailable")
			} else if !errors.Is(e, ports.ErrTVSelectionChanged) {
				t.Fatalf("business invalidation misclassified: %v", e)
			}
			if before != selectionState(t, f) {
				t.Fatal("invalidation wrote state")
			}
		})
	}
}
func TestTVSelectionObservationPGScopeAndAccount(t *testing.T) {
	f, svc, _, r := selectionFixture(t)
	for _, a := range []*ports.Actor{nil, {Type: ports.ActorTypeSystem, ID: f.actor.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleAdmin, Scopes: []string{"read"}}, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleMember}} {
		if _, e := svc.Preview(context.Background(), a, r); !errors.Is(e, ports.ErrForbidden) && !errors.Is(e, ports.ErrUnauthorized) {
			t.Fatal("bad actor", e)
		}
	}
	original := r.Packs[0]
	r.Packs[0].CandidateID = uuid.New()
	if _, e := svc.Preview(context.Background(), f.actor, r); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("review candidate scope", e)
	}
	r.Packs[0] = original
	f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID))
	if _, e := svc.Preview(context.Background(), f.actor, r); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("revocation", e)
	}
}
func TestTVSelectionObservationPGAccountWaitAndBackend(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			f, svc, _, r := selectionFixture(t)
			p := f.f.a.DBPool()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, e := p.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, e = tx.Exec(ctx, "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", uuid.MustParse(f.actor.ID)); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, e := svc.Preview(ctx, f.actor, r); done <- e }()
			waiting := false
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if e = p.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%app_user%')").Scan(&waiting); e != nil {
					t.Fatal(e)
				}
				if waiting {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("account wait not observed")
			}
			if revoke {
				if _, e = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID)); e != nil {
					t.Fatal(e)
				}
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			if revoke {
				if !errors.Is(e, ports.ErrUnavailable) {
					t.Fatal("revoked waiter got preview", e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
		})
	}
	t.Run("candidate-read-timeout", func(t *testing.T) {
		f, svc, _, r := selectionFixture(t)
		p := f.f.a.DBPool()
		tx, e := p.Begin(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, e = tx.Exec(context.Background(), "LOCK TABLE release_candidate IN ACCESS EXCLUSIVE MODE"); e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		if _, e = svc.Preview(ctx, f.actor, r); !errors.Is(e, ports.ErrUnavailable) {
			t.Fatal("backend read timeout reported business hold", e)
		}
	})

	t.Run("closed-pool", func(t *testing.T) {
		f, svc, _, r := selectionFixture(t)
		f.f.a.DBPool().Close()
		if _, e := svc.Preview(context.Background(), f.actor, r); !errors.Is(e, ports.ErrUnavailable) {
			t.Fatal(e)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		f, svc, _, r := selectionFixture(t)
		p := f.f.a.DBPool()
		tx, e := p.Begin(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, e = tx.Exec(context.Background(), "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", uuid.MustParse(f.actor.ID)); e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if _, e = svc.Preview(ctx, f.actor, r); !errors.Is(e, ports.ErrUnavailable) {
			t.Fatal("timeout not unavailable", e)
		}
	})
}
func TestTVSelectionObservationPGBoundsAndVersion(t *testing.T) {
	f, svc, repo, r := selectionFixture(t)
	observed, e := repo.ReadTVSelection(context.Background(), f.actor, r)
	if e != nil {
		t.Fatal(e)
	}
	expected := observed.Current.Version
	expected.ScopeStamp = "changed"
	r.Expected = &expected
	if _, e = svc.Preview(context.Background(), f.actor, r); !errors.Is(e, acquisition.ErrTVSelectionChanged) {
		t.Fatal("expected version", e)
	}
	r.Expected = nil
	for _, name := range []string{"duplicate", "nil-review", "too-many", "missing-mode", "nan", "inf", "season"} {
		t.Run(name, func(t *testing.T) {
			bad := r
			bad.Packs = append([]ports.TVSelectionCandidateRef(nil), r.Packs...)
			switch name {
			case "duplicate":
				bad.Packs = append(bad.Packs, bad.Packs[0])
			case "nil-review":
				bad.Packs[0].ReviewID = uuid.Nil
			case "too-many":
				bad.Packs = make([]ports.TVSelectionCandidateRef, 101)
			case "missing-mode":
				bad.PackPolicy.Mode = ""
			case "nan":
				bad.PackPolicy.MinMissingRatio = math.NaN()
			case "inf":
				bad.PackPolicy.MinMissingRatio = math.Inf(1)
			case "season":
				bad.Season = 0
			}
			if _, e := svc.Preview(context.Background(), f.actor, bad); !errors.Is(e, quality.ErrInvalidTVSelection) {
				t.Fatal(e)
			}
		})
	}
}

