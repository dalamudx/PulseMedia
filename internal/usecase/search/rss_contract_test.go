package search_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	search "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rssProbe struct {
	calls    atomic.Int32
	releases []*ports.ReleaseItem
	err      error
	started  chan struct{}
	release  chan struct{}
}

func (p *rssProbe) Search(ctx context.Context, _ string, _ []int32, _ int32, _ int32) ([]*ports.ReleaseItem, error) {
	p.calls.Add(1)
	if p.started != nil {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.releases, p.err
}
func (*rssProbe) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrUnavailable
}
func feedFixture(t *testing.T) (*roundFixture, *search.FeedService, *rssProbe) {
	t.Helper()
	f := runtimeFixture(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	p := &rssProbe{releases: []*ports.ReleaseItem{{GUID: "https://tracker.invalid/rss?passkey=feed-secret", Title: "Fight.Club.1999.2160p.Remux", DownloadURL: "https://tracker.invalid/dl?passkey=feed-secret", SizeBytes: 4096}}}
	f.svc.SetFeedClientFactory(func(_, _, _ string) ports.IndexerClient { return p })
	svc := search.NewFeedService(postgres.NewRSSStore(f.pool, idgen.New()), crypto.NewSecretProtector(key), f.svc)
	if _, e := svc.Configure(context.Background(), f.actor, f.idx, search.FeedCommand{Enabled: true, Kind: "rss", IntervalSeconds: 30, ExpectedVersion: 1, URL: "https://tracker.invalid/rss?passkey=feed-secret"}, "enable-rss"); e != nil {
		t.Fatal(e)
	}
	return f, svc, p
}
func feedJob(t *testing.T, f *roundFixture, s *search.FeedService) ports.RSSPollJob {
	t.Helper()
	ctx := context.Background()
	if n, e := s.QueueDue(ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	j, e := f.queue.Claim(ctx, []string{"rss_poll"}, "rss-owner", time.Minute)
	if e != nil || j == nil {
		t.Fatal(e)
	}
	var p ports.RSSPollJob
	if e = json.Unmarshal(j.Payload, &p); e != nil {
		t.Fatal(e)
	}
	p.ID = j.ID
	p.Owner = j.LeaseOwner.String
	p.Token = j.LeaseToken
	return p
}
func TestRSSReceiptRollbackRestartAndRoundEvidence(t *testing.T) {
	f, s, p := feedFixture(t)
	ctx := context.Background()
	round, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	job := feedJob(t, f, s)
	if _, e = f.pool.Exec(ctx, `CREATE FUNCTION reject_rss_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='indexer.rss_received' THEN RAISE EXCEPTION 'fault'; END IF; RETURN NEW; END $$;CREATE TRIGGER reject_rss_receipt BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION reject_rss_receipt()`); e != nil {
		t.Fatal(e)
	}
	if e = s.ExecuteJob(ctx, job); e == nil {
		t.Fatal("ack fault accepted")
	}
	info, e := s.Get(ctx, f.actor, f.idx)
	if e != nil || info.LastQueryID != nil {
		t.Fatal("cursor escaped transaction", info, e)
	}
	var n int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round_rss_query").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='rss_poll' AND state='running'").Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if _, e = f.pool.Exec(ctx, `DROP TRIGGER reject_rss_receipt ON outbox_event;UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE kind='rss_poll'`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.RecoverSearchJobs(ctx); e != nil {
		t.Fatal(e)
	}
	j, e := f.queue.Claim(ctx, []string{"rss_poll"}, "new-owner", time.Minute)
	if e != nil || j == nil {
		t.Fatal(e)
	}
	if e = s.ExecuteJob(ctx, job); e == nil {
		t.Fatal("old token accepted")
	}
	job.Owner = j.LeaseOwner.String
	job.Token = j.LeaseToken
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	s = search.NewFeedService(postgres.NewRSSStore(f.pool, idgen.New()), crypto.NewSecretProtector(key), f.svc)
	if e = s.ExecuteJob(ctx, job); e != nil {
		t.Fatal(e)
	}
	if p.calls.Load() != 1 {
		t.Fatal("restart re-fetched successful query", p.calls.Load())
	}
	info, e = s.Get(ctx, f.actor, f.idx)
	if e != nil || info.LastQueryID == nil || info.LastCandidateCount == nil || *info.LastCandidateCount != 1 {
		t.Fatal(info, e)
	}
	// Ordinary targeted results remain; feed adds a separate evidence source.
	j, e = f.queue.Claim(ctx, []string{"search_endpoint"}, "target", time.Minute)
	if e != nil || j == nil {
		t.Fatal(e)
	}
	if e = f.svc.ExecuteEndpointJob(ctx, endpointTask(t, j)); e != nil {
		t.Fatal(e)
	}
	rows, e := f.q.ListRoundQueryCandidates(ctx, round.ID)
	if e != nil || len(rows) != 3 {
		t.Fatal("RSS suppressed targeted results", len(rows), e)
	}
	final := f.finalJob(t, round)
	w, e := f.q.GetWantedItemByID(ctx, f.wanted)
	if e != nil {
		t.Fatal(e)
	}
	var selected uuid.UUID
	for _, c := range rows {
		if c.Guid == "correct" {
			selected = c.ID
		}
	}
	if selected == uuid.Nil {
		t.Fatal("targeted matching candidate missing")
	}
	manual := acquisition.NewService(postgres.NewAcquisitionStore(f.pool, idgen.New(), clock.New(), acquisition.SelectMovieRound))
	start := make(chan struct{})
	result := make(chan error, 2)
	var competition sync.WaitGroup
	competition.Add(2)
	go func() {
		defer competition.Done()
		<-start
		_, err := manual.AcquireMovie(ctx, f.actor, acquisition.AcquireMovieCommand{UnitIDs: []uuid.UUID{w.UnitID}, RootID: f.root, CandidateID: selected, DownloaderID: &f.downloader})
		result <- err
	}()
	go func() {
		defer competition.Done()
		<-start
		_, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(final))
		result <- err
	}()
	close(start)
	competition.Wait()
	close(result)
	for err := range result {
		if err != nil && !errors.Is(err, ports.ErrAcquisitionUnitAlreadyReserved) {
			t.Fatal("RSS/targeted/manual competition", err)
		}
	}
	var lanes int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL").Scan(&lanes); e != nil || lanes != 1 {
		t.Fatal("competing sources duplicated active acquisition", lanes, e)
	}
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	var leaks int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM release_candidate WHERE guid LIKE '%feed-secret%'").Scan(&leaks); e != nil || leaks != 0 {
		t.Fatal(leaks, e)
	}
}
func TestRSSConcurrentQueueAndEmptyReceipt(t *testing.T) {
	f, s, p := feedFixture(t)
	p.releases = nil
	ctx := context.Background()
	var group sync.WaitGroup
	var total atomic.Int32
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() { defer group.Done(); n, e := s.QueueDue(ctx, 20); total.Add(int32(n)); errs <- e }()
	}
	group.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if total.Load() != 1 {
		t.Fatal(total.Load())
	}
	j, e := f.queue.Claim(ctx, []string{"rss_poll"}, "empty", time.Minute)
	if e != nil || j == nil {
		t.Fatal(e)
	}
	var job ports.RSSPollJob
	if err := json.Unmarshal(j.Payload, &job); err != nil {
		t.Fatal(err)
	}
	job.ID = j.ID
	job.Owner = j.LeaseOwner.String
	job.Token = j.LeaseToken
	if e = s.ExecuteJob(ctx, job); e != nil {
		t.Fatal(e)
	}
	info, e := s.Get(ctx, f.actor, f.idx)
	if e != nil || info.LastCandidateCount == nil || *info.LastCandidateCount != 0 {
		t.Fatal(info, e)
	}
}
func TestRSSRateLimitNoFalseReceipt(t *testing.T) {
	f, s, p := feedFixture(t)
	p.err = ports.NewRateLimitError(2*time.Minute, "raw-secret")
	job := feedJob(t, f, s)
	ctx := context.Background()
	if e := s.ExecuteJob(ctx, job); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	if d := s.RetryDelay(ctx, f.idx); d < 110*time.Second {
		t.Fatal(d)
	}
	info, e := s.Get(ctx, f.actor, f.idx)
	if e != nil || info.LastQueryID != nil {
		t.Fatal(info, e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE indexer SET rss_next_poll_at=clock_timestamp()-interval '1 second' WHERE id=$1", f.idx); e != nil {
		t.Fatal(e)
	}
	if n, e := s.QueueDue(ctx, 20); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
func TestRSSConfigChangeDuringPhysicalRequestRejectsReceipt(t *testing.T) {
	f, s, p := feedFixture(t)
	p.started = make(chan struct{})
	p.release = make(chan struct{})
	job := feedJob(t, f, s)
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- s.ExecuteJob(ctx, job) }()
	<-p.started
	info, e := s.Configure(ctx, f.actor, f.idx, search.FeedCommand{Enabled: false, Kind: "rss", IntervalSeconds: 30, ExpectedVersion: 2}, "disable-rss")
	if e != nil || info.Enabled {
		t.Fatal(info, e)
	}
	close(p.release)
	if e = <-done; e == nil {
		t.Fatal("old config published receipt")
	}
	info, e = s.Get(ctx, f.actor, f.idx)
	if e != nil || info.LastQueryID != nil {
		t.Fatal(info, e)
	}
	if n, e := s.QueueDue(ctx, 20); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
func TestRSSQueryCannotAcknowledgeOtherQuery(t *testing.T) {
	f, s, _ := feedFixture(t)
	job := feedJob(t, f, s)
	ctx := context.Background()
	target, e := f.svc.ExecuteSearch(ctx, f.idx, "Fight Club", 0)
	if e != nil {
		t.Fatal(e)
	}
	store := postgres.NewRSSStore(f.pool, idgen.New())
	if e = store.CompleteRSS(ctx, job, &target.QueryID); !errors.Is(e, ports.ErrRSSConfigConflict) {
		t.Fatal(e)
	}
}
func TestRSSLatestReceiptFeedsFutureRound(t *testing.T) {
	f, s, _ := feedFixture(t)
	ctx := context.Background()
	job := feedJob(t, f, s)
	if e := s.ExecuteJob(ctx, job); e != nil {
		t.Fatal(e)
	}
	r, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := f.q.ListRoundQueryCandidates(ctx, r.ID)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
}

func TestRSSPhysicalLockSurvivesLeaseExpiryAndBlocksManualSearch(t *testing.T) {
	f, s, p := feedFixture(t)
	ctx := context.Background()
	job := feedJob(t, f, s)
	p.started = make(chan struct{})
	p.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- s.ExecuteJob(ctx, job) }()
	<-p.started
	if _, e := f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", job.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.ExecuteSearch(ctx, f.idx, "Fight Club", 0); !errors.Is(e, ports.ErrUnavailable) {
		close(p.release)
		<-done
		t.Fatal("manual overlapped physical RSS after logical expiry", e)
	}
	close(p.release)
	if e := <-done; e == nil {
		t.Fatal("expired lease published receipt")
	}
	info, e := s.Get(ctx, f.actor, f.idx)
	if e != nil || info.LastQueryID != nil {
		t.Fatal(info, e)
	}
	if _, e = f.svc.ExecuteSearch(ctx, f.idx, "Fight Club", 0); e != nil {
		t.Fatal("physical permit not released on real completion", e)
	}
}
func TestRSSUncommittedReceiptCannotBeConsumedInvisible(t *testing.T) {
	f, s, _ := feedFixture(t)
	ctx := context.Background()
	job := feedJob(t, f, s)
	query, e := f.svc.ExecuteRSS(ctx, f.idx, job.ID, job.ConfigVersion)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, "UPDATE indexer SET rss_last_query_id=$2,rss_last_success_at=clock_timestamp() WHERE id=$1", f.idx, query.QueryID); e != nil {
		t.Fatal(e)
	}
	// Receipt has an old timestamp but remains invisible until its transaction ends.
	bounded, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, e = f.coord.StartScheduledRound(bounded, f.wanted, time.Minute); e == nil || bounded.Err() == nil {
		t.Fatal("round consumed invisible RSS receipt", e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	r, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	var seen, ack time.Time
	if e = f.pool.QueryRow(ctx, "SELECT w.rss_seen_at,i.rss_last_success_at FROM wanted_item w,indexer i WHERE w.id=$1 AND i.id=$2", f.wanted, f.idx).Scan(&seen, &ack); e != nil || !seen.Equal(ack) {
		t.Fatal("consume cursor moved beyond visible ACK", seen, ack, e)
	}
	rows, e := f.q.ListRoundQueryCandidates(ctx, r.ID)
	if e != nil || len(rows) != 1 {
		t.Fatal("committed receipt not linked", len(rows), e)
	}
}

func TestRSSReceiptDoesNotWaitForFinalizerLockedRound(t *testing.T) {
	f, s, _ := feedFixture(t)
	ctx := context.Background()
	r, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	job := feedJob(t, f, s)
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, "SELECT id FROM search_round WHERE id=$1 FOR UPDATE", r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, "SELECT id FROM wanted_item WHERE id=$1 FOR UPDATE", f.wanted); e != nil {
		t.Fatal(e)
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if e = s.ExecuteJob(bounded, job); e != nil {
		t.Fatal("receipt waited in source/round lock inversion", e)
	}
	info, e := s.Get(ctx, f.actor, f.idx)
	if e != nil || info.LastQueryID == nil {
		t.Fatal(info, e)
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	// Even a late finalizer/evaluation overwriting the projection cannot lose RSS.
	if _, e = f.pool.Exec(ctx, "UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+interval '5 minutes' WHERE id=$1", f.wanted); e != nil {
		t.Fatal(e)
	}
	due, e := f.q.ListPendingEvaluationWantedItems(ctx, 20)
	if e != nil || len(due) != 1 || due[0].ID != f.wanted {
		t.Fatal("durable receipt lost behind overwritten projection", len(due), e)
	}
	var n int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round_rss_query WHERE round_id=$1", r.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
