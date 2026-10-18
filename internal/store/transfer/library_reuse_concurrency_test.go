package transfer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestLibraryReuseConcurrentConfirmationPG(t *testing.T) {
	f, r, a, target, file := reusePreviewFixture(t)
	c := reuseCommand(t, r, a, target, file, "hardlink")
	type result struct {
		receipt *ports.LibraryReuseReceipt
		err     error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := r.ConfirmLibraryReuse(t.Context(), a, c, "concurrent-original")
			results <- result{v, e}
		}()
	}
	wg.Wait()
	close(results)
	var first *ports.LibraryReuseReceipt
	for v := range results {
		if v.err != nil {
			t.Fatal(v.err)
		}
		if first == nil {
			first = v.receipt
		} else if *first != *v.receipt {
			t.Fatal("different receipts")
		}
	}
	if f.count(t, "library_reuse_plan") != 1 || f.count(t, "library_reuse_unit") != 1 {
		t.Fatal("duplicate demand")
	}
	if p := reusePreview(t, r, a, target); p.CanConfirm || p.WaitReason != "reserved" {
		t.Fatal("own protection absent from preview", p)
	}
	altered := c
	altered.TargetRel = "Other.mkv"
	if _, e := r.ConfirmLibraryReuse(t.Context(), a, altered, "concurrent-original"); !errors.Is(e, ports.ErrLibraryReuseChanged) {
		t.Fatal("changed original key accepted", e)
	}
	if _, e := f.pool.Exec(t.Context(), "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(a.ID)); e != nil {
		t.Fatal(e)
	}
	if _, e := r.ConfirmLibraryReuse(t.Context(), a, c, "concurrent-original"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("revoked actor obtained cached receipt", e)
	}
}

func TestLibraryReuseRemoteExclusionPG(t *testing.T) {
	for _, first := range []string{"local", "remote"} {
		t.Run(first, func(t *testing.T) {
			f, r, a, target, file := reusePreviewFixture(t)
			c := reuseCommand(t, r, a, target, file, "hardlink")
			indexer, candidate := uuid.New(), uuid.New()
			if _, e := f.q.CreateIndexer(t.Context(), db.CreateIndexerParams{ID: indexer, Name: "exclusion", Url: "https://example.test", ApiKeyEncrypted: []byte("fixture"), Categories: []byte("[]")}); e != nil {
				t.Fatal(e)
			}
			if _, e := f.q.UpsertReleaseCandidate(t.Context(), db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: indexer, Title: "Movie", Guid: "exclusion", LinkEncrypted: []byte("fixture"), QualityProfile: []byte("{}")}); e != nil {
				t.Fatal(e)
			}
			reserve := func() error {
				_, e := f.q.CreateUnitReservation(t.Context(), db.CreateUnitReservationParams{ID: uuid.New(), OperationID: uuid.New(), UnitID: target.UnitID, RootID: target.RootID, Purpose: "acquire", CandidateID: candidate, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
				return e
			}
			if first == "local" {
				if _, e := r.ConfirmLibraryReuse(t.Context(), a, c, "local"); e != nil {
					t.Fatal(e)
				}
				if e := reserve(); e == nil {
					t.Fatal("remote overlapped local")
				}
			} else {
				if e := reserve(); e != nil {
					t.Fatal(e)
				}
				if _, e := r.ConfirmLibraryReuse(t.Context(), a, c, "local"); !errors.Is(e, ports.ErrLibraryReuseChanged) {
					t.Fatal("local overlapped remote", e)
				}
			}
		})
	}
}

type heldReuseFilesystem struct {
	ports.TransferFilesystem
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (f *heldReuseFilesystem) Hardlink(ctx context.Context, source ports.TransferRoot, sourceRel string, target ports.TransferRoot, targetRel string, expected ports.FileIdentity) error {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if n == 1 {
		close(f.entered)
		<-f.release
		return f.TransferFilesystem.Hardlink(context.WithoutCancel(ctx), source, sourceRel, target, targetRel, expected)
	}
	return f.TransferFilesystem.Hardlink(ctx, source, sourceRel, target, targetRel, expected)
}
func TestLibraryReusePhysicalLeasePG(t *testing.T) {
	f, r, a, target, file := reusePreviewFixture(t)
	c := reuseCommand(t, r, a, target, file, "hardlink")
	receipt, e := r.ConfirmLibraryReuse(t.Context(), a, c, "physical")
	if e != nil {
		t.Fatal(e)
	}
	held := &heldReuseFilesystem{TransferFilesystem: f.svc.files, entered: make(chan struct{}), release: make(chan struct{})}
	f.svc.files = held
	firstCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	firstDone := make(chan error, 1)
	lease := claimReuse(t, f)
	go func() { firstDone <- r.ExecuteLibraryReuse(firstCtx, lease) }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		close(held.release)
		t.Fatal("first syscall never started")
	}
	if _, e = f.pool.Exec(t.Context(), "UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", receipt.JobID); e != nil {
		close(held.release)
		t.Fatal(e)
	}
	if _, e = f.q.RecoverLibraryReuseJobs(t.Context()); e != nil {
		close(held.release)
		t.Fatal(e)
	}
	next := claimReuse(t, f)
	secondDone := make(chan error, 1)
	go func() { secondDone <- r.ExecuteLibraryReuse(t.Context(), next) }()
	select {
	case err := <-secondDone:
		close(held.release)
		t.Fatal("new lease displaced live syscall", err)
	case <-time.After(100 * time.Millisecond):
	}
	held.mu.Lock()
	calls := held.calls
	held.mu.Unlock()
	if calls != 1 {
		close(held.release)
		t.Fatal("overlapping physical calls")
	}
	cancel()
	close(held.release)
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first call did not drain")
	}
	select {
	case e = <-secondDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not finish")
	}
	p, e := f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
	if e != nil || p.State != "completed" {
		t.Fatal(p.State, e)
	}
	if f.count(t, "library_reuse_plan") != 1 {
		t.Fatal("duplicate recovery")
	}
}
