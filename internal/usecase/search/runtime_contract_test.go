package search_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"testing"
	"time"
)

type roundFixture struct {
	svc                                *usecaseSearch.Service
	coord                              *usecaseSearch.RoundCoordinator
	pool                               *pgxpool.Pool
	q                                  *db.Queries
	queue                              *jobs.Queue
	actor                              *ports.Actor
	wanted, sub, idx, root, downloader uuid.UUID
}

func runtimeFixture(t *testing.T) *roundFixture {
	t.Helper()
	svc, pool, _, key, idx := setupSearchTest(t)
	svc.SetEndpointRepository(postgres.NewSearchEndpointStore(pool))
	gen := idgen.New()
	ctx := context.Background()
	q := db.New(pool)
	clk := clock.New()
	queue := jobs.NewQueue(pool, gen, clk)
	acq := acquisition.NewService(postgres.NewAcquisitionStore(pool, gen, clk, acquisition.SelectMovieRound))
	user := gen.NewUUID()
	if _, err := q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "owner", PasswordHash: "fixture", Role: ports.RoleAdmin, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleAdmin}
	root := gen.NewUUID()
	if _, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), MediaType: "movie", Name: "Movies"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", root); err != nil {
		t.Fatal(err)
	}
	movie := gen.NewUUID()
	if _, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: movie, Title: "Fight Club", Year: pgtype.Int4{Int32: 1999, Valid: true}, MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	unit := gen.NewUUID()
	if _, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: movie, UnitKey: "movie"}); err != nil {
		t.Fatal(err)
	}
	downloader := gen.NewUUID()
	encrypted, _ := crypto.Encrypt(key, []byte("password"))
	if _, err := q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: downloader, Name: "Downloader", ClientType: "qbittorrent", Url: "https://downloader.invalid", PasswordEncrypted: encrypted, RemotePathMap: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	sub, wanted, err := newTestSubscriptionService(pool, gen).CreateSubscription(ctx, actor, subscription.CreateSubscriptionCommand{MediaID: movie, RootID: root, ScopeType: "all", IndexerIDs: []uuid.UUID{idx}, DownloaderID: &downloader})
	if err != nil || len(wanted) != 1 {
		t.Fatal(err, wanted)
	}
	svc.RegisterClient(idx, &mockIndexerClient{releases: []*ports.ReleaseItem{
		{Title: "Fight.Club.1999.1080p.BluRay", GUID: "correct", SizeBytes: 1024, DownloadURL: "https://tracker.invalid/dl?passkey=secret"},
		{Title: "Fight.Club.1998.2160p.Remux", GUID: "wrong-year", SizeBytes: 1024, DownloadURL: "https://tracker.invalid/wrong"},
	}})
	return &roundFixture{svc, usecaseSearch.NewRoundCoordinator(postgres.NewSearchRoundStore(pool, gen), clk, acq), pool, q, queue, actor, wanted[0].ID, sub.ID, idx, root, downloader}
}
func (f *roundFixture) collect(t *testing.T) *ports.SearchRound {
	t.Helper()
	ctx := context.Background()
	round, err := f.coord.StartScheduledRound(ctx, f.wanted, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.coord.AddCandidate(ctx, round.ID, uuid.New()); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("scheduled candidate injection accepted", err)
	}
	job, err := f.queue.Claim(ctx, []string{"search_endpoint"}, "endpoint", time.Minute)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err = f.svc.ExecuteEndpointJob(ctx, endpointTask(t, job)); err != nil {
		t.Fatal(err)
	}
	rows, err := f.q.ListRoundQueryCandidates(ctx, round.ID)
	if err != nil || len(rows) != 2 {
		t.Fatal("missing per-query evidence", len(rows), err)
	}
	return round
}
func (f *roundFixture) finalJob(t *testing.T, round *ports.SearchRound) *db.Job {
	t.Helper()
	ctx := context.Background()
	if err := f.q.QueueRoundFinalization(ctx, db.QueueRoundFinalizationParams{ID: uuid.New(), ID_2: round.ID}); err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.Claim(ctx, []string{"search_round_finalize"}, "finalize", time.Minute)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	return job
}
func authority(job *db.Job) acquisition.RoundJobAuthority {
	return acquisition.RoundJobAuthority{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken}
}
func TestPersistentRoundAtomicFailureRecoveryAndConcurrentFinalization(t *testing.T) {
	f := runtimeFixture(t)
	round := f.collect(t)
	job := f.finalJob(t, round)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_round_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='completed' THEN RAISE EXCEPTION 'fixture atomic fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_round_commit BEFORE UPDATE ON search_round FOR EACH ROW EXECUTE FUNCTION reject_round_commit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job)); err == nil {
		t.Fatal("fault falsely succeeded")
	}
	for _, query := range []string{"SELECT count(*) FROM unit_reservation", "SELECT count(*) FROM job WHERE kind='download_submission'", "SELECT count(*) FROM audit_log WHERE action='acquisition.create'", "SELECT count(*) FROM outbox_event WHERE event_type='acquisition.initiated'"} {
		var n int
		if err := f.pool.QueryRow(ctx, query).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial transaction", query, n, err)
		}
	}
	persisted, err := f.q.GetSearchRoundByID(ctx, round.ID)
	if err != nil || persisted.State != "running" {
		t.Fatal(persisted, err)
	}
	if _, err := f.pool.Exec(ctx, "DROP TRIGGER reject_round_commit ON search_round"); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
			results <- err
		}()
	}
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("expected unique valid job commit", successes)
	}
	persisted, err = f.q.GetSearchRoundByID(ctx, round.ID)
	if err != nil || persisted.State != "completed" || !persisted.OperationID.Valid {
		t.Fatal(persisted, err)
	}
	chosen, err := f.q.GetCandidateByID(ctx, persisted.SelectedCandidateID.Bytes)
	if err != nil || chosen.Guid != "correct" {
		t.Fatal(chosen.Guid, err)
	}
	var n int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
func TestRoundStaleTokenCannotAcquireAndReclaimRecovers(t *testing.T) {
	f := runtimeFixture(t)
	round := f.collect(t)
	job := f.finalJob(t, round)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job)); err == nil {
		t.Fatal("expired job acquired")
	}
	if _, err := f.q.RecoverSearchJobs(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.queue.Claim(ctx, []string{"search_round_finalize"}, "restart", time.Minute)
	if err != nil || fresh == nil || fresh.LeaseToken <= job.LeaseToken {
		t.Fatal(fresh, err)
	}
	if _, err = f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job)); err == nil {
		t.Fatal("old token acquired")
	}
	if _, err = f.coord.FinalizeLeasedRound(ctx, round.ID, authority(fresh)); err != nil {
		t.Fatal(err)
	}
}
func TestRoundSnapshotRejectsChangedIntentRootAndEndpoints(t *testing.T) {
	for _, tc := range []struct{ name, sql, reason string }{
		{"pause", "UPDATE subscription SET state='paused',intent_version=intent_version+1 WHERE id=$1", "subscription_paused"},
		{"intent", "UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1", "intent_version_changed"},
		{"policy", "UPDATE subscription SET quality_policy_revision=quality_policy_revision+1 WHERE id=$1", "policy_revision_changed"},
		{"bindings", "UPDATE subscription SET indexer_ids='[]'::jsonb WHERE id=$1", "target_or_bindings_changed"},
		{"root", "UPDATE library_root SET generation=generation+1 WHERE id=$1", "root_changed"},
		{"indexer", "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", "endpoint_changed"},
		{"downloader", "UPDATE download_client SET url='https://changed.invalid' WHERE id=$1", "downloader_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := runtimeFixture(t)
			round := f.collect(t)
			job := f.finalJob(t, round)
			id := f.sub
			if tc.name == "root" {
				id = f.root
			}
			if tc.name == "indexer" {
				id = f.idx
			}
			if tc.name == "downloader" {
				id = f.downloader
			}
			if _, err := f.pool.Exec(context.Background(), tc.sql, id); err != nil {
				t.Fatal(err)
			}
			rep, err := f.coord.FinalizeLeasedRound(context.Background(), round.ID, authority(job))
			if !errors.Is(err, usecaseSearch.ErrRoundAborted) || rep.Reason != tc.reason {
				t.Fatal(rep, err)
			}
			var n int
			if err = f.pool.QueryRow(context.Background(), "SELECT count(*) FROM unit_reservation").Scan(&n); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}
func TestRoundPauseLinearizesBeforeAcquisition(t *testing.T) {
	f := runtimeFixture(t)
	round := f.collect(t)
	job := f.finalJob(t, round)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "UPDATE subscription SET state='paused',intent_version=intent_version+1 WHERE id=$1", f.sub); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job)); result <- err }()
	select {
	case err := <-result:
		t.Fatal("finalize bypassed subscription row lock", err)
	case <-time.After(40 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, usecaseSearch.ErrRoundAborted) {
		t.Fatal(err)
	}
}
