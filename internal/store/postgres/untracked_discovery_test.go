package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type untrackedDiscoveryFixture struct {
	*untrackedFixture
	discovery *postgres.UntrackedDiscoveryStore
}

// Use latest rather than the schema102-only fixture constructor.
func newUntrackedDiscoveryFixture(t *testing.T, kinds ...string) *untrackedDiscoveryFixture {
	t.Helper()
	return newUntrackedDiscoveryFixtureAt(t, postgres.CurrentMigrationVersion, kinds...)
}

func newUntrackedDiscoveryFixtureAt(t *testing.T, through int64, kinds ...string) *untrackedDiscoveryFixture {
	t.Helper()
	kind := "movie"
	if len(kinds) > 1 {
		t.Fatal("one fixture media type expected")
	}
	if len(kinds) == 1 {
		kind = kinds[0]
	}
	if kind != "movie" && kind != "tv" {
		t.Fatal("invalid fixture media type")
	}
	f := &untrackedFixture{checkinFixture: newCheckinFixtureAt(t, repositoryMigrationsThrough(t, through), through), scope: uuid.New(), root: uuid.New(), client: uuid.New()}
	f.exec(t, `INSERT INTO library_root(id,path,name,media_type,health,baseline_completed) VALUES($1,'/scope-target','discovery',$2,'online',true)`, f.root, kind)
	f.exec(t, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'discovery','qbittorrent','https://scope.invalid','fixture','fixture','{"/remote":"/scope-source"}')`, f.client)
	scan := uuid.New()
	f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, f.root)
	f.exec(t, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,111,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, f.root)
	f.in = ports.UntrackedScopeInput{Enabled: true, ClientID: f.client, RemotePrefix: "/remote", TargetRootID: f.root}
	if err := f.p.QueryRow(t.Context(), `SELECT c.updated_at,r.generation FROM download_client c CROSS JOIN library_root r WHERE c.id=$1 AND r.id=$2`, f.client, f.root).Scan(&f.in.ExpectedClientVersion, &f.in.ExpectedRootGeneration); err != nil {
		t.Fatal(err)
	}
	f.store = postgres.NewUntrackedScopeStore(f.p, idgen.New(), hintClock{})
	f.save(t, f.in, "discovery-enable")
	return &untrackedDiscoveryFixture{untrackedFixture: f, discovery: postgres.NewUntrackedDiscoveryStore(f.p, idgen.New(), hintClock{})}
}

func (f *untrackedDiscoveryFixture) request(t *testing.T) *ports.UntrackedDiscoveryScheduleResult {
	t.Helper()
	r, err := f.discovery.RequestUntrackedDiscovery(t.Context(), f.scope, 1)
	if err != nil || r == nil || r.JobID == uuid.Nil {
		t.Fatal(r, err)
	}
	return r
}

func (f *untrackedDiscoveryFixture) claim(t *testing.T) ports.UntrackedDiscoveryCapture {
	t.Helper()
	j, err := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.UntrackedDiscoveryJobKind}, Column2: "discovery-worker", Column3: 120000})
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.discovery.LoadUntrackedDiscovery(t.Context(), ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken})
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	return c
}

func (f *untrackedDiscoveryFixture) count(t *testing.T, sql string, want int, args ...any) {
	t.Helper()
	var n int
	if err := f.p.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil || n != want {
		t.Fatalf("count=%d want=%d err=%v SQL=%s", n, want, err, sql)
	}
}

func discoverySample() []ports.UntrackedFileObservation {
	return []ports.UntrackedFileObservation{{RelativePath: "external/movie.mkv", Identity: ports.FileIdentity{Device: 2, Inode: 333, Size: 100, Mtime: 200}}}
}

func (f *untrackedDiscoveryFixture) files(t *testing.T) []ports.UntrackedDiscoveredFile {
	t.Helper()
	p, err := f.discovery.ReadUntrackedDiscovery(t.Context(), f.a, f.scope, "", 64)
	if err != nil || !p.Complete || p.NextCursor != "" {
		t.Fatal(p, err)
	}
	return p.Files
}

func discoveryBusinessFacts(t *testing.T, f *untrackedDiscoveryFixture) string {
	t.Helper()
	return publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM unit_fact x),(SELECT jsonb_agg(to_jsonb(x)) FROM wanted_item x),(SELECT jsonb_agg(to_jsonb(x)) FROM download_task x),(SELECT jsonb_agg(to_jsonb(x)) FROM library_file x),(SELECT jsonb_agg(to_jsonb(x)) FROM library_file_unit x),(SELECT jsonb_agg(to_jsonb(x)) FROM untracked_directory_scope_receipt x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x))::text`)
}

func TestUntrackedDiscoveryRequestRestartDirtyAndSweepPG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	before := discoveryBusinessFacts(t, f)
	first := f.request(t)
	if !first.Queued {
		t.Fatal(first)
	}
	second := f.request(t)
	if second.Queued || first.JobID != second.JobID {
		t.Fatal(first, second)
	}
	c := f.claim(t)
	if c.UntrackedDiscoverySnapshot().Sequence != 1 {
		t.Fatal(c.UntrackedDiscoverySnapshot())
	}
	old := f.discovery
	f.discovery = postgres.NewUntrackedDiscoveryStore(f.p, idgen.New(), hintClock{})
	checkinErr(t, f.discovery.ValidateUntrackedDiscovery(t.Context(), c), ports.ErrRejected)
	lease := c.UntrackedDiscoverySnapshot().Lease
	restarted, err := f.discovery.LoadUntrackedDiscovery(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.discovery.CompleteUntrackedDiscovery(t.Context(), restarted); err != nil {
		t.Fatal(err)
	}
	checkinErr(t, old.RecordUntrackedDiscovery(t.Context(), c, discoverySample()), ports.ErrUntrackedDiscoveryStale)
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_discovery' AND state='queued'`, 1)
	next := f.claim(t)
	if next.UntrackedDiscoverySnapshot().Sequence != 2 || next.UntrackedDiscoverySnapshot().Lease.ID == lease.ID {
		t.Fatal(next.UntrackedDiscoverySnapshot())
	}
	if err = f.discovery.CompleteUntrackedDiscovery(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if n, e := f.discovery.QueueDueUntrackedDiscoveries(t.Context(), 64); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	f.count(t, `SELECT count(*) FROM untracked_discovery_state WHERE next_sweep_at>clock_timestamp()+interval '50 seconds' AND requested_sequence=completed_sequence AND active_job_id IS NULL`, 1)
	f.exec(t, `UPDATE untracked_discovery_state SET next_sweep_at=clock_timestamp()-interval '1 second' WHERE scope_id=$1`, f.scope)
	if n, e := f.discovery.QueueDueUntrackedDiscoveries(t.Context(), 64); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if discoveryBusinessFacts(t, f) != before {
		t.Fatal("discovery changed protected facts/receipts")
	}
}

func TestUntrackedDiscoveryObservationsIdempotentChangedAndNoNegativePG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	before := discoveryBusinessFacts(t, f)
	f.request(t)
	c := f.claim(t)
	snapshot := c.UntrackedDiscoverySnapshot()
	if snapshot.Scope.SourceIdentity == snapshot.Scope.TargetIdentity || snapshot.Scope.SourceIdentity.Inode != 222 || snapshot.Scope.TargetIdentity.Inode != 111 {
		t.Fatal("independent source identity lost", snapshot)
	}
	// The public value cannot mutate the repository's capture.
	snapshot.Scope.SourceIdentity.Inode = 999
	snapshot.Lease.Owner = "forged"
	if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, discoverySample()); err != nil {
		t.Fatal(err)
	}
	initial := f.files(t)
	if len(initial) != 1 || initial[0].Observations != 1 || initial[0].Revision != 1 {
		t.Fatal(initial)
	}
	for _, sample := range [][]ports.UntrackedFileObservation{discoverySample(), nil, {{RelativePath: "external/movie.mkv", Identity: ports.FileIdentity{Device: 2, Inode: 999, Size: 200, Mtime: 300}}}} {
		if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, sample); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(initial, f.files(t)) {
			t.Fatal("same execution retry changed projection")
		}
	}
	if err := f.discovery.CompleteUntrackedDiscovery(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.request(t)
	c = f.claim(t)
	if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, discoverySample()); err != nil {
		t.Fatal(err)
	}
	same := f.files(t)
	if same[0].Revision != 1 || same[0].Observations != 2 || same[0].ID != initial[0].ID || !same[0].FirstObservedAt.Equal(initial[0].FirstObservedAt) {
		t.Fatal(initial, same)
	}
	if err := f.discovery.CompleteUntrackedDiscovery(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.request(t)
	c = f.claim(t)
	changed := discoverySample()
	changed[0].Identity.Size++
	if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, changed); err != nil {
		t.Fatal(err)
	}
	after := f.files(t)
	if after[0].Revision != 2 || after[0].Observations != 1 || after[0].ID != initial[0].ID || !after[0].FirstObservedAt.After(initial[0].FirstObservedAt) {
		t.Fatal(initial, after)
	}
	if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.discovery.CompleteUntrackedDiscovery(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, f.files(t)) || discoveryBusinessFacts(t, f) != before {
		t.Fatal("empty scan deleted observations or wrote facts")
	}
}

func TestUntrackedDiscoveryLeaseRecoveryBoundedPG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	job := f.request(t).JobID
	old := f.claim(t)
	if err := f.discovery.RecordUntrackedDiscovery(t.Context(), old, discoverySample()); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job)
		checkinErr(t, f.discovery.ValidateUntrackedDiscovery(t.Context(), old), ports.ErrUntrackedDiscoveryStale)
		checkinErr(t, f.discovery.CompleteUntrackedDiscovery(t.Context(), old), ports.ErrUntrackedDiscoveryStale)
		n, err := f.discovery.QueueDueUntrackedDiscoveries(t.Context(), 64)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 3 {
			if n != 0 {
				t.Fatal("exhausted job spawned unbounded retry", n)
			}
			f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='dead' AND attempts=3`, 1, job)
			break
		}
		if n != 1 {
			t.Fatal(n)
		}
		c := f.claim(t)
		if c.UntrackedDiscoverySnapshot().Lease.ID != job || c.UntrackedDiscoverySnapshot().Lease.Token <= old.UntrackedDiscoverySnapshot().Lease.Token {
			t.Fatal("recovery changed identity or reused lease")
		}
		checkinErr(t, f.discovery.RecordUntrackedDiscovery(t.Context(), old, discoverySample()), ports.ErrUntrackedDiscoveryStale)
		if err = f.discovery.RecordUntrackedDiscovery(t.Context(), c, discoverySample()); err != nil {
			t.Fatal(err)
		}
		old = c
	}
	if f.files(t)[0].Observations != 3 {
		t.Fatal(f.files(t))
	}
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_discovery'`, 1)
	other := uuid.New()
	f.exec(t, `INSERT INTO job(id,kind,state,payload,max_attempts,attempts,lease_owner,lease_token,lease_until) VALUES($1,'other_r0','running','{}',3,1,'other',1,clock_timestamp()-interval '1 second')`, other)
	if _, err := f.discovery.QueueDueUntrackedDiscoveries(t.Context(), 64); err != nil {
		t.Fatal(err)
	}
	f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='running' AND lease_owner='other'`, 1, other)
}

func TestUntrackedDiscoveryConcurrentRequestsWorkersAndCompletionPG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	const workers = 8
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, errs[i] = f.discovery.RequestUntrackedDiscovery(ctx, f.scope, 1) }()
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_discovery'`, 1)
	f.count(t, `SELECT count(*) FROM untracked_discovery_state WHERE requested_sequence=8`, 1)
	c := f.claim(t)
	if _, err := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.UntrackedDiscoveryJobKind}, Column2: "other", Column3: 120000}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("second claim", err)
	}
	start = make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				_, errs[i] = f.discovery.QueueDueUntrackedDiscoveries(ctx, 64)
			} else {
				errs[i] = f.discovery.CompleteUntrackedDiscovery(ctx, c)
			}
		}()
	}
	close(start)
	wg.Wait()
	successfulCompletion := false
	for i, err := range errs {
		if i%2 == 1 && err == nil {
			successfulCompletion = true
		}
		if err != nil && !errors.Is(err, ports.ErrUntrackedDiscoveryStale) {
			t.Fatal(err)
		}
	}
	if !successfulCompletion {
		t.Fatal("no completion won")
	}
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_discovery' AND state='queued'`, 1)
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_discovery'`, 2)
}

func TestUntrackedDiscoveryRejectsNoncanonicalBatchAndCancellationPG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	f.request(t)
	c := f.claim(t)
	for _, p := range []string{"", ".", "../movie.mkv", "/movie.mkv", "a//movie.mkv", "a/../movie.mkv", "a\\movie.mkv", "a\nmovie.mkv", "movie.nfo", string([]byte{255}) + ".mkv"} {
		sample := discoverySample()
		sample[0].RelativePath = p
		checkinErr(t, f.discovery.RecordUntrackedDiscovery(t.Context(), c, sample), ports.ErrRejected)
	}
	for _, identity := range []ports.FileIdentity{{Device: -1, Inode: 1}, {Inode: 0}, {Inode: 1, Size: -1}} {
		sample := discoverySample()
		sample[0].Identity = identity
		checkinErr(t, f.discovery.RecordUntrackedDiscovery(t.Context(), c, sample), ports.ErrRejected)
	}
	checkinErr(t, f.discovery.RecordUntrackedDiscovery(t.Context(), c, make([]ports.UntrackedFileObservation, 129)), ports.ErrRejected)
	duplicate := append(discoverySample(), discoverySample()...)
	checkinErr(t, f.discovery.RecordUntrackedDiscovery(t.Context(), c, duplicate), ports.ErrRejected)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	checkinErr(t, f.discovery.RecordUntrackedDiscovery(ctx, c, discoverySample()), context.Canceled)
	_, err := f.discovery.RequestUntrackedDiscovery(ctx, f.scope, 1)
	checkinErr(t, err, context.Canceled)
	f.count(t, `SELECT count(*) FROM untracked_discovered_file`, 0)
}
