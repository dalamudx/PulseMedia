package download_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type budgetBlockedClient struct {
	ports.DownloadClient
	entered chan struct{}
	release <-chan struct{}
}

func (c *budgetBlockedClient) Test(context.Context) error {
	c.entered <- struct{}{}
	<-c.release
	return nil
}

type budgetSubmissionFence struct {
	ports.DownloadSubmissionRepository
	prepares atomic.Int32
}

func (s *budgetSubmissionFence) PrepareSubmission(context.Context, ports.DownloadSubmissionCommand) (*ports.DownloadSubmissionReceipt, error) {
	s.prepares.Add(1)
	return nil, errors.New("unexpected preparation before admission")
}

type blockedTorrentSource struct {
	ports.IndexerClient
	entered chan<- struct{}
	release <-chan struct{}
}

func (s blockedTorrentSource) FetchTorrent(context.Context, string) ([]byte, error) {
	s.entered <- struct{}{}
	<-s.release
	return []byte("d4:info" + ordinaryFixtureInfo + "e"), nil
}

func TestSourceCancellationRetainsPhysicalBudgetUntilReturn(t *testing.T) {
	mgr, q, _, id, candidate := setupDownloadTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}, 2), make(chan struct{})
	source := blockedTorrentSource{entered: entered, release: release}
	// The existing inspector is not reached after cancellation.
	mgr.SetTorrentSource(func(_, _, _ string) ports.IndexerClient { return source }, torrent.Inspector{})
	mgr.RegisterClient(id, &mockDownloadClient{torrents: map[string]*ports.TorrentInfo{}})
	owner, cancelOwner := context.WithCancel(ctx)
	defer cancelOwner()
	done := make(chan error, 2)
	ops := []uuid.UUID{uuid.New(), uuid.New()}
	for _, op := range ops {
		go func() { _, err := mgr.SubmitDownload(owner, op, id, candidate, "/configured/downloads"); done <- err }()
	}
	var once sync.Once
	drain := func() {
		once.Do(func() {
			close(release)
			for range 2 {
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Errorf("source cancellation: %v", err)
				}
			}
		})
	}
	defer drain()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("source not entered")
		}
	}
	cancelOwner()
	waiting, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	_, _, err := mgr.TestClient(waiting, id)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("source released physical budget before return", err)
	}
	drain()
	for _, op := range ops {
		if _, err = q.GetDownloadTaskByOpID(ctx, op); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("cancelled source created task", err)
		}
	}
	if healthy, _, err := mgr.TestClient(ctx, id); err != nil || !healthy {
		t.Fatal("source budget did not recover", err)
	}
}

func TestDownloaderCancelledAdmissionNeverMutatesTask(t *testing.T) {
	mgr, q, _, id, candidate := setupDownloadTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	trackOp, unknownOp := uuid.New(), uuid.New()
	for _, p := range []db.CreateDownloadTaskParams{
		{ID: uuid.New(), OperationID: trackOp, ClientID: id, CandidateID: candidate, InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "tracking", SavePath: "/configured/downloads"},
		{ID: uuid.New(), OperationID: unknownOp, ClientID: id, CandidateID: candidate, InfoHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", State: "unknown", SavePath: "/configured/downloads"},
	} {
		if _, e := q.CreateDownloadTask(ctx, p); e != nil {
			t.Fatal(e)
		}
	}
	before := map[uuid.UUID]string{}
	for _, op := range []uuid.UUID{trackOp, unknownOp} {
		row, e := q.GetDownloadTaskByOpID(ctx, op)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := json.Marshal(row)
		if e != nil {
			t.Fatal(e)
		}
		before[op] = string(raw)
	}
	hold := make(chan struct{})
	blocked := &budgetBlockedClient{entered: make(chan struct{}, 2), release: hold}
	mgr.RegisterClient(id, blocked)
	owner, cancelOwner := context.WithCancel(ctx)
	defer cancelOwner()
	done := make(chan error, 2)
	go func() { _, _, e := mgr.TestClient(owner, id); done <- e }()
	go func() { _, _, e := mgr.TestClient(ctx, id); done <- e }()
	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			close(hold)
			cancelled := 0
			for range 2 {
				if e := <-done; errors.Is(e, context.Canceled) {
					cancelled++
				} else if e != nil {
					t.Errorf("owner: %v", e)
				}
			}
			if cancelled != 1 {
				t.Errorf("cancelled owners: got %d, want 1", cancelled)
			}
		})
	}
	defer finish()
	for range 2 {
		select {
		case <-blocked.entered:
		case <-ctx.Done():
			t.Fatal("physical owner did not start")
		}
	}
	cancelOwner()
	mgr.RegisterClient(id, &mockDownloadClient{torrents: map[string]*ports.TorrentInfo{}})
	fence := &budgetSubmissionFence{}
	mgr.SetSubmissionRepository(fence)
	independent := uuid.New()
	mgr.RegisterClient(independent, &mockDownloadClient{})
	independentCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	healthy, _, e := mgr.TestClient(independentCtx, independent)
	stop()
	if e != nil || !healthy {
		t.Fatalf("independent client blocked: %v", e)
	}
	for _, method := range []string{"test", "submit", "authorized_submit", "reconcile", "observe"} {
		t.Run(method, func(t *testing.T) {
			wait, stop := context.WithTimeout(ctx, 25*time.Millisecond)
			defer stop()
			op := uuid.New()
			var e error
			switch method {
			case "test":
				_, _, e = mgr.TestClient(wait, id)
			case "submit":
				_, e = mgr.SubmitDownload(wait, op, id, candidate, "/configured/downloads")
			case "authorized_submit":
				_, e = mgr.SubmitDownload(wait, op, id, candidate, "/configured/downloads", ports.JobLease{ID: uuid.New(), Owner: "owner", Token: 1})
			case "reconcile":
				_, e = mgr.ReconcileTask(wait, unknownOp)
			case "observe":
				_, e = mgr.ObserveTask(wait, trackOp)
			}
			if !errors.Is(e, context.DeadlineExceeded) {
				t.Fatalf("waiting %s returned %v", method, e)
			}
			if method == "submit" || method == "authorized_submit" {
				if _, e = q.GetDownloadTaskByOpID(ctx, op); !errors.Is(e, pgx.ErrNoRows) {
					t.Fatalf("unissued submission wrote task: %v", e)
				}
			}
		})
	}
	if fence.prepares.Load() != 0 {
		t.Fatal("cancelled admission entered submission transaction")
	}
	for op, original := range before {
		row, e := q.GetDownloadTaskByOpID(ctx, op)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := json.Marshal(row)
		if e != nil {
			t.Fatal(e)
		}
		if string(raw) != original {
			t.Fatal("cancelled admission changed existing task")
		}
	}
	finish()
	if healthy, _, e := mgr.TestClient(ctx, id); e != nil || !healthy {
		t.Fatalf("capacity did not recover: %v", e)
	}
	op := uuid.New()
	task, e := mgr.SubmitDownload(ctx, op, id, candidate, "/configured/downloads")
	if e != nil || task == nil || task.State != "tracking" {
		t.Fatalf("admitted submission did not recover: %v", e)
	}
	if replay, e := mgr.SubmitDownload(ctx, op, id, candidate, "/configured/downloads"); e != nil || replay == nil || replay.ID != task.ID {
		t.Fatalf("receipt reuse after admission: %v", e)
	}
}
