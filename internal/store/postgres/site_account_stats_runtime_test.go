package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func runtimeStats(t *testing.T) (*statsFixture, *postgres.SiteAccountStatsStore) {
	t.Helper()
	f := newStatsFixture(t)
	f.save(t, f.input(), "runtime")
	return f, postgres.NewSiteAccountStatsStore(f.p, idgen.New(), f.secrets)
}
func claimStats(t *testing.T, f *statsFixture) ports.JobLease {
	t.Helper()
	j, e := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.SiteAccountStatsJobKind}, Column2: "stats-worker", Column3: 120000})
	if e != nil {
		t.Fatal(e)
	}
	return ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
}
func statsCount(t *testing.T, f *statsFixture, sql string, want int, args ...any) {
	t.Helper()
	var n int
	if e := f.p.QueryRow(t.Context(), sql, args...).Scan(&n); e != nil || n != want {
		t.Fatalf("count=%d want=%d err=%v", n, want, e)
	}
}
func statsSample(uid string) *ports.SiteAccountStats {
	return &ports.SiteAccountStats{AccountID: uid, Ratio: indexer.SiteAccountRatio{State: indexer.SiteRatioUnknown}}
}
func statsPermit(t *testing.T, f *statsFixture, s *postgres.SiteAccountStatsStore) (ports.SiteAccountStatsPermit, ports.JobLease) {
	t.Helper()
	if e := s.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	l := claimStats(t, f)
	p, e := s.Acquire(t.Context(), l)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	return p, l
}
func TestSiteAccountStatsRuntimeSchedulingPG(t *testing.T) {
	f, s := runtimeStats(t)
	// The source account is global; disabling its settings author does not stop scheduling.
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, f.a.ID)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- s.EnqueueDue(t.Context()) })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	statsCount(t, f, `SELECT count(*) FROM job WHERE kind='site_account_stats_collect'`, 1)
	statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt WHERE state='queued'`, 1)
	statsCount(t, f, `SELECT count(*) FROM site_account_stats_config_receipt`, 1)
	if e := s.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	statsCount(t, f, `SELECT count(*) FROM job WHERE kind='site_account_stats_collect'`, 1)
}
func TestSiteAccountStatsRuntimeSamplesAndHistoryPG(t *testing.T) {
	for _, failure := range []error{ports.ErrAuthRequired, ports.ErrBlocked, ports.ErrBadResponse, ports.ErrUnavailable, nil} {
		t.Run(statsFailureName(failure), func(t *testing.T) {
			f, s := runtimeStats(t)
			p, _ := statsPermit(t, f, s)
			if e := p.Reserve(t.Context()); e != nil {
				t.Fatal(e)
			}
			if e := p.Reserve(t.Context()); e == nil {
				t.Fatal("second reservation accepted")
			}
			if e := p.Finish(t.Context(), statsSample(f.input().ExpectedAccountID), nil); e != nil {
				t.Fatal(e)
			}
			p.Close()
			f.exec(t, `UPDATE site_account_stats_schedule SET next_due_at=clock_timestamp()-interval '1 second'`)
			f.exec(t, `UPDATE indexer SET next_query_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.id)
			p, _ = statsPermit(t, f, s)
			if e := p.Reserve(t.Context()); e != nil {
				t.Fatal(e)
			}
			sample := statsSample("1")
			if failure != nil {
				sample = nil
			}
			if e := p.Finish(t.Context(), sample, failure); e != nil {
				t.Fatal(e)
			}
			statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt WHERE state='succeeded' AND sample->>'account_id'=$1`, 1, f.input().ExpectedAccountID)
			statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt WHERE state='failed' AND sample IS NULL`, 1)
			for _, sql := range []string{`UPDATE site_account_stats_attempt SET failure_code='blocked'`, `DELETE FROM site_account_stats_attempt`, `TRUNCATE site_account_stats_attempt`, `UPDATE site_account_stats_attempt SET sample='{}' WHERE state='succeeded'`} {
				if _, e := f.p.Exec(t.Context(), sql); e == nil {
					t.Fatal("history modification accepted", sql)
				}
			}
		})
	}
}
func statsFailureName(e error) string {
	if e == nil {
		return "wrong_uid"
	}
	switch e {
	case ports.ErrAuthRequired:
		return "auth"
	case ports.ErrBlocked:
		return "blocked"
	case ports.ErrBadResponse:
		return "bad_response"
	}
	return "unavailable"
}
func TestSiteAccountStatsRuntimeBindingPG(t *testing.T) {
	for _, change := range []string{"credentials", "config", "disable", "source_version", "owner", "token", "isolation"} {
		t.Run(change, func(t *testing.T) {
			f, s := runtimeStats(t)
			p, l := statsPermit(t, f, s)
			if e := p.Reserve(t.Context()); e != nil {
				t.Fatal(e)
			}
			switch change {
			case "credentials":
				f.exec(t, `UPDATE indexer SET api_key_encrypted='changed' WHERE id=$1`, f.id)
			case "config":
				in := f.input()
				in.ExpectedVersion = 1
				in.IntervalSeconds = 901
				f.save(t, in, "changed")
			case "disable":
				in := f.input()
				in.ExpectedVersion = 1
				in.Enabled = false
				f.save(t, in, "disabled")
			case "source_version":
				f.exec(t, `UPDATE indexer SET config_version=config_version+1 WHERE id=$1`, f.id)
			case "owner":
				f.exec(t, `UPDATE job SET lease_owner='another' WHERE id=$1`, l.ID)
			case "token":
				f.exec(t, `UPDATE job SET lease_token=lease_token+1 WHERE id=$1`, l.ID)
			case "isolation":
				f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id='00000000-0000-0000-0000-000000000001' WHERE singleton`)
			}
			if e := p.Check(t.Context()); e == nil {
				t.Fatal("changed binding accepted")
			}
			if e := p.Finish(t.Context(), statsSample(f.input().ExpectedAccountID), nil); e == nil {
				t.Fatal("late callback accepted")
			}
			statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt WHERE sample IS NOT NULL`, 0)
		})
	}
}
func TestSiteAccountStatsRuntimeBudgetPG(t *testing.T) {
	for _, reserved := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "remote429"}[reserved], func(t *testing.T) {
			f, s := runtimeStats(t)
			p, _ := statsPermit(t, f, s)
			var failure error
			if !reserved {
				f.exec(t, `UPDATE indexer SET next_query_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, f.id)
			}
			failure = p.Reserve(t.Context())
			if reserved {
				if failure != nil {
					t.Fatal(failure)
				}
				failure = ports.NewRateLimitError(37*time.Second, "private upstream material must not persist")
			} else if _, ok := ports.IsRateLimited(failure); !ok {
				t.Fatal(failure)
			}
			if e := p.Finish(t.Context(), nil, failure); e != nil {
				t.Fatal(e)
			}
			statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt WHERE state='failed' AND failure_code='rate_limited' AND sample IS NULL`, 1)
			if reserved {
				statsCount(t, f, `SELECT count(*) FROM indexer WHERE rate_limit_until>clock_timestamp()+interval '35 seconds' AND next_query_at>=rate_limit_until`, 1)
			} else {
				statsCount(t, f, `SELECT count(*) FROM site_account_stats_schedule WHERE next_due_at<clock_timestamp()+interval '4 seconds'`, 1)
			}
		})
	}
}
func TestSiteAccountStatsRuntimePhysicalGatePG(t *testing.T) {
	f, s := runtimeStats(t)
	if e := s.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	l := claimStats(t, f)
	release, e := postgres.AcquireIndexerPermit(t.Context(), f.p, f.id)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	_, e = s.Acquire(ctx, l)
	cancel()
	release()
	if e == nil {
		t.Fatal("search gate bypassed")
	}
	p, e := s.Acquire(t.Context(), l)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = p.Reserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	assertStatsGateHeld(t, f)
	ctx, cancel = context.WithCancel(t.Context())
	cancel()
	if e = p.Check(ctx); e == nil {
		t.Fatal("cancelled check accepted")
	}
	// A logical cancellation must not release the physical gate.
	assertStatsGateHeld(t, f)
}
func TestSiteAccountStatsRuntimeRecoveryPG(t *testing.T) {
	f, s := runtimeStats(t)
	p, l := statsPermit(t, f, s)
	if e := p.Reserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID)
	if e := p.Finish(t.Context(), statsSample(f.input().ExpectedAccountID), nil); e == nil {
		t.Fatal("expired result accepted")
	}
	if e := s.RecoverExpired(t.Context()); e != nil {
		t.Fatal(e)
	}
	statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt WHERE state='review_required'`, 1)
	if e := s.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	statsCount(t, f, `SELECT count(*) FROM job WHERE kind='site_account_stats_collect'`, 1)
	assertStatsGateHeld(t, f)
	in := f.input()
	in.ExpectedVersion = 1
	f.save(t, in, "operator_resume")
	if e := s.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	statsCount(t, f, `SELECT count(*) FROM job WHERE kind='site_account_stats_collect'`, 2)
	p.Close()
}
func TestSiteAccountStatsRuntimePIDLossPG(t *testing.T) {
	f, s := runtimeStats(t)
	p, l := statsPermit(t, f, s)
	if e := p.Reserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	var killed bool
	e := f.p.QueryRow(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND granted AND objsubid=1 AND classid=((hashtextextended('indexer:'||$1::text,0)>>32)&4294967295)::oid AND objid=(hashtextextended('indexer:'||$1::text,0)&4294967295)::oid AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`, f.id.String()).Scan(&killed)
	if e != nil || !killed {
		t.Fatal(killed, e)
	}
	if e = p.Finish(t.Context(), statsSample(f.input().ExpectedAccountID), nil); e == nil {
		t.Fatal("lost session committed")
	}
	p.Close()
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID)
	if e = s.RecoverExpired(t.Context()); e != nil {
		t.Fatal(e)
	}
	statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt WHERE state='review_required' AND sample IS NULL`, 1)
}
func TestSiteAccountStatsRuntimeDeferredClockPG(t *testing.T) {
	f, s := runtimeStats(t)
	p, l := statsPermit(t, f, s)
	if e := p.Reserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	// This deferred trigger runs after the application tail check, before the stats fence.
	f.exec(t, `CREATE FUNCTION stats_test_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.2); RETURN NULL; END $$`)
	f.exec(t, `CREATE CONSTRAINT TRIGGER a_stats_test_pause AFTER UPDATE ON site_account_stats_attempt DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION stats_test_pause()`)
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '100 milliseconds' WHERE id=$1`, l.ID)
	if e := p.Finish(t.Context(), statsSample(f.input().ExpectedAccountID), nil); e == nil {
		t.Fatal("deferred expired commit accepted")
	}
	statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt WHERE sample IS NOT NULL`, 0)
	statsCount(t, f, `SELECT count(*) FROM job WHERE id=$1 AND state='running'`, 1, l.ID)
}

func TestSiteAccountStatsRuntimeHealthProjectionPG(t *testing.T) {
	f, s := runtimeStats(t)
	p, _ := statsPermit(t, f, s)
	if e := p.Reserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `UPDATE indexer SET health='offline',health_checked_at=clock_timestamp(),health_observed_at=clock_timestamp(),health_failures=health_failures+1,health_reason='unavailable' WHERE id=$1`, f.id)
	if e := p.Check(t.Context()); e != nil {
		t.Fatalf("health projection invalidated current execution: %v", e)
	}
	if e := p.Finish(t.Context(), statsSample(f.input().ExpectedAccountID), nil); e != nil {
		t.Fatal(e)
	}
	statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt WHERE state='succeeded'`, 1)
}
func assertStatsGateHeld(t *testing.T, f *statsFixture) {
	t.Helper()
	release, e := postgres.AcquireIndexerPermit(t.Context(), f.p, f.id)
	if release != nil {
		release()
	}
	if !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("physical gate was released", e)
	}
}
