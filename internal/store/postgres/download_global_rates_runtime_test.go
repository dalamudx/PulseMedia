package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type globalRateClock struct{}

func (globalRateClock) Now() time.Time                  { return time.Now() }
func (globalRateClock) Since(t time.Time) time.Duration { return time.Since(t) }

type globalRateFixture struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	store  *postgres.DownloadGlobalRatesStore
	queue  *jobs.Queue
	client uuid.UUID
}

func newGlobalRateFixture(t *testing.T) *globalRateFixture {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(t.Context(), dsn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ids := idgen.New()
	cipher := crypto.NewSecretProtector(make([]byte, 32))
	encoded, err := cipher.EncryptSecret([]byte("owned-native-reader"))
	if err != nil {
		t.Fatal(err)
	}
	f := &globalRateFixture{pool: pool, q: db.New(pool), client: ids.NewUUID()}
	if _, err = f.q.CreateDownloadClient(t.Context(), db.CreateDownloadClientParams{ID: f.client, Name: "global observer", ClientType: "qbittorrent", Url: "http://127.0.0.1/owned", Username: "reader", PasswordEncrypted: encoded, RemotePathMap: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	f.store = postgres.NewDownloadGlobalRatesStore(pool, ids, cipher)
	f.queue = jobs.NewQueue(pool, ids, globalRateClock{})
	return f
}
func (f *globalRateFixture) claim(t *testing.T) ports.JobLease {
	t.Helper()
	if err := f.store.EnqueueDue(t.Context()); err != nil {
		t.Fatal("schedule global rate", err)
	}
	job, err := f.queue.Claim(t.Context(), []string{ports.DownloadGlobalRatesJobKind}, "owned-global-reader", time.Minute)
	if err != nil || job == nil {
		t.Fatal("claim global rate", err)
	}
	return ports.JobLease{ID: job.ID, Owner: "owned-global-reader", Token: job.LeaseToken}
}
func (f *globalRateFixture) acquire(t *testing.T, lease ports.JobLease) ports.DownloadGlobalRatesPermit {
	t.Helper()
	p, err := f.store.Acquire(t.Context(), lease)
	if err != nil {
		t.Fatal("acquire global rate", err)
	}
	t.Cleanup(p.Close)
	return p
}
func globalRateSample() ports.DownloadGlobalRatesObservation {
	down, up := int64(123), int64(456)
	return ports.DownloadGlobalRatesObservation{ObservedAt: time.Now().UTC(), Rates: &ports.DownloadGlobalRates{DownloadBytesPerSecond: &down, UploadBytesPerSecond: &up}}
}
func (f *globalRateFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *globalRateFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *globalRateFixture) due(t *testing.T) {
	t.Helper()
	f.exec(t, `UPDATE download_global_rate_schedule SET next_due_at=clock_timestamp()-interval '1 second' WHERE client_id=$1`, f.client)
}

func TestDownloadGlobalRatesLifecyclePG(t *testing.T) {
	f := newGlobalRateFixture(t)
	lease := f.claim(t)
	p := f.acquire(t, lease)
	source := p.Snapshot()
	if source.ClientID != f.client || len(source.ConfigStamp) != 64 || source.Password != "owned-native-reader" {
		t.Fatal("incorrect private native material")
	}
	raw, err := json.Marshal(source)
	if err != nil || string(raw) != "{}" {
		t.Fatal("private source escaped JSON")
	}
	if err = p.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	sample := globalRateSample()
	if err = p.Finish(t.Context(), sample); err != nil {
		t.Fatal("seal global rates", err)
	}
	if f.count(t, `SELECT count(*) FROM download_global_rate_attempt a JOIN download_global_rate_schedule s ON s.client_id=a.client_id JOIN job j ON j.id=a.job_id WHERE a.job_id=$1 AND a.state='succeeded' AND a.download_bytes_per_second=123 AND a.upload_bytes_per_second=456 AND s.active_job_id IS NULL AND s.latest_job_id=a.job_id AND s.last_good_job_id=a.job_id AND j.state='succeeded'`, lease.ID) != 1 {
		t.Fatal("sample/schedule/job did not commit together")
	}
	if err = p.Finish(t.Context(), sample); err == nil {
		t.Fatal("terminal permit accepted duplicate publication")
	}
	p.Close()
	if err = f.store.EnqueueDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM download_global_rate_attempt`) != 1 {
		t.Fatal("sampling interval ignored")
	}
}

func TestDownloadGlobalRatesFailurePreservesLastGoodPG(t *testing.T) {
	f := newGlobalRateFixture(t)
	first := f.claim(t)
	p := f.acquire(t, first)
	if err := p.Finish(t.Context(), globalRateSample()); err != nil {
		t.Fatal(err)
	}
	p.Close()
	f.due(t)
	second := f.claim(t)
	p = f.acquire(t, second)
	if err := p.Finish(t.Context(), ports.DownloadGlobalRatesObservation{ObservedAt: time.Now().UTC(), Failure: ports.DownloadGlobalRatesLimited, RetryAfter: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM download_global_rate_schedule s JOIN download_global_rate_attempt latest ON latest.job_id=s.latest_job_id JOIN download_global_rate_attempt good ON good.job_id=s.last_good_job_id WHERE s.client_id=$1 AND latest.job_id=$2 AND latest.state='failed' AND latest.failure_code='rate_limited' AND latest.download_bytes_per_second IS NULL AND latest.upload_bytes_per_second IS NULL AND good.job_id=$3 AND good.download_bytes_per_second=123 AND good.upload_bytes_per_second=456 AND s.next_due_at>clock_timestamp()+interval '50 seconds'`, f.client, second.ID, first.ID) != 1 {
		t.Fatal("failed observation replaced last-good values or retry interval")
	}
}

func TestDownloadGlobalRatesConfigChangesRejectLateSamplePG(t *testing.T) {
	f := newGlobalRateFixture(t)
	lease := f.claim(t)
	p := f.acquire(t, lease)
	f.exec(t, `UPDATE download_client SET url=url||'/changed' WHERE id=$1`, f.client)
	if err := p.Check(t.Context()); !errors.Is(err, ports.ErrEndpointVersionConflict) {
		t.Fatal("changed source remained readable", err)
	}
	if err := p.Finish(t.Context(), globalRateSample()); !errors.Is(err, ports.ErrEndpointVersionConflict) {
		t.Fatal("late sample accepted changed source", err)
	}
	if f.count(t, `SELECT count(*) FROM download_global_rate_attempt WHERE observed_at IS NOT NULL`) != 0 {
		t.Fatal("late values were published")
	}
}

func TestDownloadGlobalRatesLeaseFencingPG(t *testing.T) {
	for _, kind := range []string{"owner", "token", "expired", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			f := newGlobalRateFixture(t)
			lease := f.claim(t)
			if kind == "owner" || kind == "token" {
				wrong := lease
				if kind == "owner" {
					wrong.Owner = "stale-worker"
				} else {
					wrong.Token++
				}
				if p, err := f.store.Acquire(t.Context(), wrong); err == nil {
					p.Close()
					t.Fatal("foreign lease acquired source")
				}
				if f.count(t, `SELECT count(*) FROM download_global_rate_attempt WHERE state='queued' AND execution_owner IS NULL`) != 1 {
					t.Fatal("foreign lease changed execution")
				}
				return
			}
			p := f.acquire(t, lease)
			ctx := t.Context()
			if kind == "expired" {
				f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, lease.ID)
			} else {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			if err := p.Finish(ctx, globalRateSample()); err == nil {
				t.Fatal("invalid lease/context published")
			}
			if f.count(t, `SELECT count(*) FROM download_global_rate_attempt WHERE observed_at IS NOT NULL`) != 0 {
				t.Fatal("late sample persisted")
			}
		})
	}
}

func TestDownloadGlobalRatesRecoverWaitsForPhysicalReturnPG(t *testing.T) {
	f := newGlobalRateFixture(t)
	lease := f.claim(t)
	p := f.acquire(t, lease)
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, lease.ID)
	if err := f.store.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM download_global_rate_attempt WHERE state='running' AND job_id=$1`, lease.ID) != 1 {
		t.Fatal("logical expiration released physical source")
	}
	if err := f.store.EnqueueDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM download_global_rate_attempt`) != 1 {
		t.Fatal("new read queued while original I/O owned source")
	}
	p.Close()
	if err := f.store.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM download_global_rate_attempt a JOIN download_global_rate_schedule s ON s.client_id=a.client_id JOIN job j ON j.id=a.job_id WHERE a.job_id=$1 AND a.state='abandoned' AND s.active_job_id IS NULL AND s.latest_job_id=a.job_id AND j.state='dead'`, lease.ID) != 1 {
		t.Fatal("interrupted source not marked unknown atomically")
	}
	f.due(t)
	next := f.claim(t)
	if next.ID == lease.ID {
		t.Fatal("original execution lease was reused")
	}
}

func TestDownloadGlobalRatesConcurrentSchedulingPG(t *testing.T) {
	f := newGlobalRateFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.store.EnqueueDue(t.Context()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.count(t, `SELECT count(*) FROM job WHERE kind='download_global_rates'`) != 1 || f.count(t, `SELECT count(*) FROM download_global_rate_attempt WHERE state='queued'`) != 1 {
		t.Fatal("concurrent schedulers duplicated physical work")
	}
}

func TestDownloadGlobalRatesTailRollbackPG(t *testing.T) {
	f := newGlobalRateFixture(t)
	lease := f.claim(t)
	p := f.acquire(t, lease)
	f.exec(t, `CREATE FUNCTION pulse_test_global_rate_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected global rate tail rollback'; END $$`)
	f.exec(t, `CREATE CONSTRAINT TRIGGER global_rate_test_tail AFTER UPDATE ON download_global_rate_attempt DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_test_global_rate_tail()`)
	sample := globalRateSample()
	if err := p.Finish(t.Context(), sample); err == nil {
		t.Fatal("tail failure unexpectedly committed")
	}
	if f.count(t, `SELECT count(*) FROM download_global_rate_attempt a JOIN download_global_rate_schedule s ON s.client_id=a.client_id JOIN job j ON j.id=a.job_id WHERE a.job_id=$1 AND a.state='running' AND a.observed_at IS NULL AND s.active_job_id=a.job_id AND s.latest_job_id IS NULL AND j.state='running'`, lease.ID) != 1 {
		t.Fatal("tail rollback leaked partial observation/schedule/job")
	}
	f.exec(t, `DROP TRIGGER global_rate_test_tail ON download_global_rate_attempt`)
	if err := p.Finish(t.Context(), sample); err != nil {
		t.Fatal("original lease could not seal the same observation", err)
	}
}

func TestDownloadGlobalRatesCommitClockLeasePG(t *testing.T) {
	f := newGlobalRateFixture(t)
	lease := f.claim(t)
	p := f.acquire(t, lease)
	f.exec(t, `CREATE FUNCTION pulse_test_global_rate_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.25); RETURN NULL; END $$`)
	f.exec(t, `CREATE CONSTRAINT TRIGGER a_global_rate_delay AFTER UPDATE ON download_global_rate_attempt DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_test_global_rate_delay()`)
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '150 milliseconds' WHERE id=$1`, lease.ID)
	if err := p.Finish(t.Context(), globalRateSample()); err == nil {
		t.Fatal("lease expiring during deferred work committed")
	}
	if f.count(t, `SELECT count(*) FROM download_global_rate_attempt WHERE observed_at IS NOT NULL`) != 0 {
		t.Fatal("commit-time lease failure published values")
	}
}
