package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type runtimeCheckinSecrets struct{ key []byte }

func (s runtimeCheckinSecrets) DecryptSecret(b []byte) ([]byte, error) {
	return crypto.Decrypt(s.key, b)
}

type runtimeCheckinFixture struct {
	p, p2   *pgxpool.Pool
	s, s2   *postgres.SiteCheckinStore
	id      uuid.UUID
	a       *ports.Actor
	secrets runtimeCheckinSecrets
	dsn     string
}

func runtimeCheckin(t *testing.T) *runtimeCheckinFixture {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	v, e := postgres.Migrate(t.Context(), dsn, "../../../migrations")
	if e != nil || v != 100 {
		t.Fatal(v, e)
	}
	p, e := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	p2, e := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p2.Close)
	f := &runtimeCheckinFixture{p: p, p2: p2, id: uuid.New(), secrets: runtimeCheckinSecrets{bytes.Repeat([]byte{49}, 32)}, dsn: dsn}
	f.s = postgres.NewSiteCheckinStore(p, idgen.New(), f.secrets)
	f.s2 = postgres.NewSiteCheckinStore(p2, idgen.New(), f.secrets)
	u, e := db.New(p).CreateUser(t.Context(), db.CreateUserParams{ID: uuid.New(), Username: uuid.NewString(), PasswordHash: "fixture", Role: "admin", IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	f.a = &ports.Actor{Type: ports.ActorTypeUser, ID: u.ID.String(), Role: "admin"}
	f.source(t, f.id)
	return f
}
func (f *runtimeCheckinFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, e := f.p.Exec(t.Context(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func (f *runtimeCheckinFixture) source(t *testing.T, id uuid.UUID) {
	t.Helper()
	cookie, e := crypto.Encrypt(f.secrets.key, []byte("session=fixture"))
	if e != nil {
		t.Fatal(e)
	}
	url, e := crypto.Encrypt(f.secrets.key, []byte("https://nexus.test/root/"))
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO indexer(id,name,url,api_key_encrypted)VALUES($1,'checkin','https://nexus.test',$2)`, id, cookie)
	f.exec(t, `INSERT INTO indexer_configuration(indexer_id,protocol,url_encrypted,behavior_version,configuration_version)VALUES($1,'nexusphp',$2,'explicit-v1',1)`, id, url)
	_, e = postgres.NewSiteCheckinConfigStore(f.p, idgen.New(), f.secrets).SaveSiteCheckinConfig(t.Context(), f.a, id, ports.SiteCheckinConfigInput{ExpectedAccountID: "42", Profile: ports.NexusPHP1103CheckinProfile, Timezone: "Asia/Shanghai", DailyAt: "00:00", Enabled: true, ExpectedSourceVersion: 1}, "runtime")
	if e != nil {
		t.Fatal(e)
	}
}
func (f *runtimeCheckinFixture) count(t *testing.T, sql string, want int, args ...any) {
	t.Helper()
	var n int
	if e := f.p.QueryRow(t.Context(), sql, args...).Scan(&n); e != nil || n != want {
		t.Fatalf("count %d want %d err %v: %s", n, want, e, sql)
	}
}
func (f *runtimeCheckinFixture) claim(t *testing.T, kind string) ports.JobLease {
	t.Helper()
	j, e := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{kind}, Column2: "checkin-worker", Column3: 120000})
	if e != nil {
		t.Fatal(e)
	}
	return ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
}
func (f *runtimeCheckinFixture) permit(t *testing.T) (ports.SiteCheckinPermit, ports.JobLease) {
	t.Helper()
	if e := f.s.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	l := f.claim(t, ports.SiteCheckinJobKind)
	p, e := f.s.Acquire(t.Context(), l)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	return p, l
}
func (f *runtimeCheckinFixture) budget(t *testing.T) {
	f.exec(t, `UPDATE indexer SET next_query_at=clock_timestamp()-interval '1 second',rate_limit_until=NULL WHERE id=$1`, f.id)
}
func pendingCheckin() ports.CheckinObservation {
	return ports.CheckinObservation{AccountID: "42", Mark: ports.CheckinMarkPending}
}
func attendedCheckin() *ports.CheckinObservation {
	return &ports.CheckinObservation{AccountID: "42", Mark: ports.CheckinMarkAttended}
}
func prepareCheckin(t *testing.T, f *runtimeCheckinFixture, p ports.SiteCheckinPermit) ports.SiteCheckinSendPermission {
	t.Helper()
	if _, e := p.ReserveObserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.budget(t)
	g, e := p.PrepareSubmit(t.Context(), pendingCheckin())
	if e != nil {
		t.Fatal(e)
	}
	return g
}
func TestSiteCheckinRuntimeConcurrentSchedulingPG(t *testing.T) {
	f := runtimeCheckin(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			s := f.s
			if i%2 == 1 {
				s = f.s2
			}
			errs <- s.EnqueueDue(t.Context())
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	f.count(t, `SELECT count(*) FROM site_checkin_day`, 1)
	f.count(t, `SELECT count(*) FROM job WHERE kind='site_checkin'`, 1)
	p, l := f.permit(t)
	if _, e := f.s2.Acquire(t.Context(), l); e == nil {
		t.Fatal("second physical gate acquired")
	}
	p.Close()
}
func TestSiteCheckinRuntimePreparedAndFrozenPG(t *testing.T) {
	for _, outcome := range []string{"attended", "unknown", "not_sent", "observation_only_bypass", "wrong_grant", "wrong_uid"} {
		t.Run(outcome, func(t *testing.T) {
			f := runtimeCheckin(t)
			p, _ := f.permit(t)
			g := prepareCheckin(t, f, p)
			if _, e := p.PrepareSubmit(t.Context(), pendingCheckin()); e == nil {
				t.Fatal("second send permission")
			}
			q := db.New(f.p)
			blocked, e := q.SiteCheckinRestoreBlocked(t.Context())
			if e != nil || !blocked {
				t.Fatal(blocked, e)
			}
			for _, tc := range []struct {
				uid, zone string
				want      bool
			}{{"42", "UTC", true}, {"42", "Asia/Shanghai", false}, {"43", "UTC", false}, {"42", "UTC", true}} {
				v, e := q.SiteCheckinConflictingCalendar(t.Context(), db.SiteCheckinConflictingCalendarParams{IndexerID: f.id, ExpectedAccountID: tc.uid, Timezone: tc.zone})
				if e != nil || v != tc.want {
					t.Fatal(tc, v, e)
				}
			}
			sub := &ports.CheckinSubmission{State: ports.CheckinSubmissionUnknown}
			out := ports.SiteCheckinOutcome{Permission: &g, Submission: sub}
			switch outcome {
			case "attended":
				sub.State = ports.CheckinSubmissionAttended
				sub.Observation = attendedCheckin()
			case "not_sent":
				sub.State = ports.CheckinSubmissionNotSent
			case "observation_only_bypass":
				out.Submission = nil
				out.Observation = attendedCheckin()
			case "wrong_grant":
				bad := g
				bad.ID = uuid.New()
				out.Permission = &bad
			case "wrong_uid":
				sub.State = ports.CheckinSubmissionAttended
				sub.Observation = attendedCheckin()
				sub.Observation.AccountID = "43"
			}
			e = p.Finish(t.Context(), out)
			if outcome == "wrong_grant" {
				if e == nil {
					t.Fatal("wrong grant accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			want := 0
			if outcome == "attended" {
				want = 1
			}
			f.count(t, `SELECT count(*) FROM site_checkin_day WHERE status='attended_observed'`, want)
			f.count(t, `SELECT count(*) FROM site_checkin_day WHERE prepared_execution_id IS NOT NULL`, 1)
			for _, sql := range []string{`DELETE FROM site_checkin_day`, `TRUNCATE site_checkin_day CASCADE`, `UPDATE site_checkin_day SET prepared_execution_id=NULL`, `UPDATE site_checkin_execution SET permission_id=NULL`, `UPDATE site_checkin_execution SET result='{"tampered":true}' WHERE finished_at IS NOT NULL`} {
				if _, e = f.p.Exec(t.Context(), sql); e == nil {
					t.Fatal("tamper accepted", sql)
				}
			}
			if e = p.Finish(t.Context(), out); e == nil {
				t.Fatal("old receipt mutated")
			}
		})
	}
}
func TestSiteCheckinRuntimePreflightAndBudgetPG(t *testing.T) {
	f := runtimeCheckin(t)
	p, _ := f.permit(t)
	if _, e := p.ReserveObserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	if _, e := p.ReserveObserve(t.Context()); e == nil {
		t.Fatal("repeated GET reservation")
	}
	if g, e := p.PrepareSubmit(t.Context(), pendingCheckin()); e == nil || g.ID != uuid.Nil {
		t.Fatal("POST without separate budget", g, e)
	} else if _, ok := ports.IsRateLimited(e); !ok {
		t.Fatal(e)
	}
	if e := p.Finish(t.Context(), ports.SiteCheckinOutcome{Observation: attendedCheckin()}); e != nil {
		t.Fatal(e)
	}
	f.count(t, `SELECT count(*) FROM site_checkin_day WHERE status='attended_observed' AND prepared_execution_id IS NULL`, 1)
}
func TestSiteCheckinRuntimeRecoveryPG(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprint(prepared), func(t *testing.T) {
			f := runtimeCheckin(t)
			p, l := f.permit(t)
			if prepared {
				prepareCheckin(t, f, p)
			}
			p.Close()
			f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID)
			if e := f.s.RecoverExpired(t.Context()); e != nil {
				t.Fatal(e)
			}
			if !prepared {
				f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='queued' AND attempts=1 AND lease_token=$2`, 1, l.ID, l.Token)
				next := f.claim(t, ports.SiteCheckinJobKind)
				if next.Token <= l.Token {
					t.Fatal("lease token reset")
				}
				p, e := f.s.Acquire(t.Context(), next)
				if e != nil {
					t.Fatal(e)
				}
				p.Close()
				return
			}
			if e := f.s.RecoverExpired(t.Context()); e != nil {
				t.Fatal(e)
			}
			f.count(t, `SELECT count(*) FROM job WHERE kind='site_checkin_observe' AND max_attempts=1`, 1)
			l = f.claim(t, ports.SiteCheckinObservationJobKind)
			p, e := f.s.Acquire(t.Context(), l)
			if e != nil {
				t.Fatal(e)
			}
			defer p.Close()
			if !p.Snapshot().ObservationOnly {
				t.Fatal("recovery can send")
			}
			if g, e := p.PrepareSubmit(t.Context(), pendingCheckin()); e == nil || g.ID != uuid.Nil {
				t.Fatal("R3 send granted")
			}
			f.budget(t)
			if _, e = p.ReserveObserve(t.Context()); e != nil {
				t.Fatal(e)
			}
			if e = p.Finish(t.Context(), ports.SiteCheckinOutcome{Observation: attendedCheckin()}); e != nil {
				t.Fatal(e)
			}
			f.count(t, `SELECT count(*) FROM site_checkin_execution WHERE prepared_at IS NOT NULL`, 1)
			f.count(t, `SELECT count(*) FROM site_checkin_day WHERE status='attended_observed'`, 1)
		})
	}
}
func TestSiteCheckinRuntimeDriftAndHealthPG(t *testing.T) {
	for _, change := range []string{"health", "scalar", "cipher", "uid", "config"} {
		t.Run(change, func(t *testing.T) {
			f := runtimeCheckin(t)
			p, _ := f.permit(t)
			switch change {
			case "health":
				f.exec(t, `UPDATE indexer SET next_query_at=clock_timestamp()+interval '3 seconds' WHERE id=$1`, f.id)
			case "scalar":
				f.exec(t, `UPDATE indexer SET url='https://other.test' WHERE id=$1`, f.id)
			case "cipher":
				f.exec(t, `UPDATE indexer SET api_key_encrypted='\\x01' WHERE id=$1`, f.id)
			case "uid":
				f.exec(t, `UPDATE site_checkin_config SET expected_account_id='43' WHERE indexer_id=$1`, f.id)
			case "config":
				f.exec(t, `UPDATE site_checkin_config SET daily_at='01:00' WHERE indexer_id=$1`, f.id)
			}
			_, e := p.Check(t.Context())
			if (e == nil) != (change == "health") {
				t.Fatal(change, e)
			}
		})
	}
}
func TestSiteCheckinRuntimeCommitRollbackPG(t *testing.T) {
	for _, phase := range []string{"prepare", "finish"} {
		t.Run(phase, func(t *testing.T) {
			f := runtimeCheckin(t)
			p, l := f.permit(t)
			if _, e := p.ReserveObserve(t.Context()); e != nil {
				t.Fatal(e)
			}
			f.budget(t)
			// Fault injection occurs inside the transaction immediately before deferred validation.
			f.exec(t, `CREATE FUNCTION checkin_test_expire() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=NEW.job_id; RETURN NEW; END $$`)
			f.exec(t, `CREATE TRIGGER zz_checkin_test_expire AFTER UPDATE ON site_checkin_execution FOR EACH ROW EXECUTE FUNCTION checkin_test_expire()`)
			if phase == "prepare" {
				g, e := p.PrepareSubmit(t.Context(), pendingCheckin())
				if e == nil || g.ID != uuid.Nil {
					t.Fatal("expired COMMIT granted send", g, e)
				}
				f.count(t, `SELECT count(*) FROM site_checkin_day WHERE prepared_execution_id IS NOT NULL`, 0)
			} else {
				e := p.Finish(t.Context(), ports.SiteCheckinOutcome{Observation: attendedCheckin()})
				if e == nil {
					t.Fatal("expired positive published")
				}
				f.count(t, `SELECT count(*) FROM site_checkin_execution WHERE finished_at IS NOT NULL`, 0)
			}
			f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='running' AND lease_until>clock_timestamp()`, 1, l.ID)
		})
	}
}
func TestSiteCheckinRuntimeCalendarAndDigestPG(t *testing.T) {
	f := runtimeCheckin(t)
	q := db.New(f.p)
	for _, tc := range []struct {
		date  string
		hours int
	}{{"2026-03-08T12:00:00Z", 23}, {"2026-11-01T12:00:00Z", 25}} {
		instant, e := time.Parse(time.RFC3339, tc.date)
		if e != nil {
			t.Fatal(e)
		}
		c, e := q.SiteCheckinCalendar(t.Context(), db.SiteCheckinCalendarParams{Instant: pgtype.Timestamptz{Time: instant, Valid: true}, Zone: "America/New_York", Daily: "00:00"})
		if e != nil || c.DayEnd.Time.Sub(c.DayStart.Time) != time.Duration(tc.hours)*time.Hour {
			t.Fatal(c, e)
		}
	}
	conn, e := f.p.Acquire(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	var first string
	for _, zone := range []string{"UTC", "Asia/Shanghai"} {
		tx, e := conn.Begin(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(t.Context(), `SELECT set_config('TimeZone',$1,true)`, zone); e != nil {
			t.Fatal(e)
		}
		b, e := db.New(tx).SiteCheckinBinding(t.Context(), f.id)
		if e != nil {
			t.Fatal(e)
		}
		if first != "" && b != first {
			t.Fatal("session timezone changes digest")
		}
		first = b
		if e = tx.Rollback(t.Context()); e != nil {
			t.Fatal(e)
		}
	}
}
func TestSiteCheckinRuntimeCancelRetainsGatePG(t *testing.T) {
	f := runtimeCheckin(t)
	p, l := f.permit(t)
	prepareCheckin(t, f, p)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := p.Check(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	conn, e := f.p2.Acquire(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	held, e := db.New(conn).TrySiteAccountStatsGate(t.Context(), f.id)
	if e != nil || held {
		t.Fatal("cancellation released physical gate", held, e)
	}
	if e = p.Finish(ctx, ports.SiteCheckinOutcome{Observation: attendedCheckin()}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	f.count(t, `SELECT count(*) FROM site_checkin_day WHERE prepared_execution_id IS NOT NULL AND status='prepared'`, 1)
	p.Close()
	if _, e = f.s2.Acquire(t.Context(), l); e == nil {
		t.Fatal("prepared day reacquired send authority")
	}
}
func TestSiteCheckinRuntimeSchedulerBatchPG(t *testing.T) {
	f := runtimeCheckin(t)
	for range 64 {
		f.source(t, uuid.New())
	}
	if e := f.s.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.count(t, `SELECT count(*) FROM site_checkin_day`, 64)
	ids, e := db.New(f.p).ListSiteCheckinSources(t.Context())
	if e != nil || len(ids) != 1 {
		t.Fatal(len(ids), e)
	}
	if e = f.s.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.count(t, `SELECT count(*) FROM site_checkin_day`, 65)
}
func TestSiteCheckinRuntimeBoundedR0PG(t *testing.T) {
	f := runtimeCheckin(t)
	p, l := f.permit(t)
	p.Close()
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			l = f.claim(t, ports.SiteCheckinJobKind)
			p, e := f.s.Acquire(t.Context(), l)
			if e != nil {
				t.Fatal(e)
			}
			p.Close()
		}
		f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID)
		if e := f.s.RecoverExpired(t.Context()); e != nil {
			t.Fatal(e)
		}
	}
	f.count(t, `SELECT count(*) FROM job WHERE kind='site_checkin' AND state='dead' AND attempts=3 AND max_attempts=3`, 1)
	f.count(t, `SELECT count(*) FROM site_checkin_execution WHERE state='not_sent'`, 3)
	f.count(t, `SELECT count(*) FROM job WHERE kind='site_checkin_observe'`, 0)
	if e := f.s.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.count(t, `SELECT count(*) FROM job`, 1)
}
func TestSiteCheckinRuntimeR3PendingIsFinalPG(t *testing.T) {
	f := runtimeCheckin(t)
	p, _ := f.permit(t)
	g := prepareCheckin(t, f, p)
	if e := p.Finish(t.Context(), ports.SiteCheckinOutcome{Permission: &g, Submission: &ports.CheckinSubmission{State: ports.CheckinSubmissionUnknown}}); e != nil {
		t.Fatal(e)
	}
	p.Close()
	if e := f.s.RecoverExpired(t.Context()); e != nil {
		t.Fatal(e)
	}
	l := f.claim(t, ports.SiteCheckinObservationJobKind)
	p, e := f.s.Acquire(t.Context(), l)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	f.budget(t)
	if _, e = p.ReserveObserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e = p.Finish(t.Context(), ports.SiteCheckinOutcome{Submission: &ports.CheckinSubmission{State: ports.CheckinSubmissionAttended, Observation: attendedCheckin()}}); e == nil {
		t.Fatal("R3 accepted submission")
	}
	pending := pendingCheckin()
	if e = p.Finish(t.Context(), ports.SiteCheckinOutcome{Observation: &pending}); e != nil {
		t.Fatal(e)
	}
	if e = f.s.RecoverExpired(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.count(t, `SELECT count(*) FROM job WHERE kind='site_checkin_observe'`, 1)
	f.count(t, `SELECT count(*) FROM site_checkin_day WHERE status='review_required' AND prepared_execution_id IS NOT NULL`, 1)
}
func TestSiteCheckinRuntimePIDLossPG(t *testing.T) {
	f := runtimeCheckin(t)
	p, _ := f.permit(t)
	g := prepareCheckin(t, f, p)
	var pid int
	if e := f.p.QueryRow(t.Context(), `SELECT backend_pid FROM site_checkin_execution WHERE id=$1`, g.ExecutionID).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `SELECT pg_terminate_backend($1)`, pid)
	if e := p.Finish(t.Context(), ports.SiteCheckinOutcome{Permission: &g, Submission: &ports.CheckinSubmission{State: ports.CheckinSubmissionAttended, Observation: attendedCheckin()}}); e == nil {
		t.Fatal("late PID callback published")
	}
	p.Close()
	f.count(t, `SELECT count(*) FROM site_checkin_day WHERE status='prepared' AND prepared_execution_id IS NOT NULL`, 1)
	blocked, e := db.New(f.p).SiteCheckinRestoreBlocked(t.Context())
	if e != nil || !blocked {
		t.Fatal(blocked, e)
	}
}
func TestSiteCheckinRuntimeOfflineReconciliationPG(t *testing.T) {
	f := runtimeCheckin(t)
	p, l := f.permit(t)
	g := prepareCheckin(t, f, p)
	p.Close()
	q := db.New(f.p)
	root := uuid.New()
	f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id=$1 WHERE singleton`, root)
	params := db.MarkSiteCheckinRestoreDecisionParams{RestoreID: uuid.New(), Operator: "operator", Evidence: "physical I/O ended, authority retained", DayID: g.DayID}
	if n, e := q.MarkSiteCheckinRestoreDecision(t.Context(), params); e != nil || n != 0 {
		t.Fatal("live day jobs reconciled", n, e)
	}
	f.exec(t, `UPDATE job SET state='dead' WHERE id=$1`, l.ID)
	if _, e := q.MarkSiteCheckinRestoreDecision(t.Context(), params); e == nil {
		t.Fatal("wrong restore root accepted")
	}
	params.RestoreID = root
	n, e := q.MarkSiteCheckinRestoreDecision(t.Context(), params)
	if e != nil || n != 1 {
		t.Fatal(n, e)
	}
	d, e := q.GetSiteCheckinRestoreDay(t.Context(), g.DayID)
	if e != nil || !d.PreparedExecutionID.Valid || d.PreparedExecutionID.Bytes != g.ExecutionID || d.RestoreID.Bytes != root || d.Status != "prepared" {
		t.Fatal(d, e)
	}
	blocked, e := q.SiteCheckinRestoreBlocked(t.Context())
	if e != nil || blocked {
		t.Fatal(blocked, e)
	}
	if n, e = q.MarkSiteCheckinRestoreDecision(t.Context(), params); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	f.exec(t, `UPDATE restore_isolation SET isolated=false WHERE singleton`)
	if e = f.s.RecoverExpired(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e = f.s.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.count(t, `SELECT count(*) FROM job WHERE kind='site_checkin_observe'`, 0)
	f.count(t, `SELECT count(*) FROM site_checkin_execution`, 1)
	if _, e = f.s.Acquire(t.Context(), l); e == nil {
		t.Fatal("reconciled execution acquired")
	}
}
func TestSiteCheckinRuntimeRateLimitSharedPG(t *testing.T) {
	f := runtimeCheckin(t)
	p, _ := f.permit(t)
	if _, e := p.ReserveObserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e := p.Finish(t.Context(), ports.SiteCheckinOutcome{Failure: ports.NewRateLimitError(time.Minute, "private failure")}); e != nil {
		t.Fatal(e)
	}
	f.count(t, `SELECT count(*) FROM indexer WHERE id=$1 AND next_query_at>clock_timestamp()+interval '50 seconds' AND rate_limit_until>clock_timestamp()+interval '50 seconds'`, 1, f.id)
	f.count(t, `SELECT count(*) FROM site_checkin_execution WHERE safe_code='rate_limited' AND result='{}'`, 1)
}
