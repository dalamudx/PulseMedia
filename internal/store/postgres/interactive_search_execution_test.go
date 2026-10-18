package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

type interactiveBlockedClient struct {
	entered   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
	once      sync.Once
	calls     atomic.Int32
}

func (c *interactiveBlockedClient) Search(ctx context.Context, _ string, _ []int32, _, _ int32) ([]*ports.ReleaseItem, error) {
	c.calls.Add(1)
	c.once.Do(func() { close(c.entered) })
	// Context expiry is observable, but this physical operation cannot return yet.
	select {
	case <-ctx.Done():
		close(c.cancelled)
		<-c.release
	case <-c.release:
	}
	return []*ports.ReleaseItem{{Title: "Film.2024.1080p.WEB-DL.H264", GUID: "physical-source-item", DownloadURL: "https://site.test/torrent", SizeBytes: 512, Seeders: 3}}, nil
}
func (*interactiveBlockedClient) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrRejected
}

func TestInteractiveSearchPhysicalExclusionAfterCancellationAndLeaseLossPG(t *testing.T) {
	for _, scenario := range []string{"cancel", "lease-loss", "network-timeout"} {
		t.Run(scenario, func(t *testing.T) {
			p, _, actor, repo := interactiveFixture(t)
			ctx, stop := context.WithTimeout(t.Context(), 20*time.Second)
			defer stop()
			site := interactiveSite(t, p, "Blocked source", "offline", true, false)
			first := interactiveCreate(t, repo, actor, "first-physical-search")
			task := interactiveClaim(t, p, first, site)
			lease, err := repo.LoadInteractiveSearchSource(ctx, task)
			if err != nil {
				t.Fatal("load original authority", err)
			}
			codec := crypto.NewSecretProtector(bytes.Repeat([]byte{19}, 32))
			executor := search.NewService(postgres.NewSearchExecutionStore(p), interactiveIDs{}, codec, 0)
			client := &interactiveBlockedClient{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
			executor.RegisterClient(site, client)
			call, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			var release sync.Once
			joined := false
			go func() { done <- executor.ExecuteInteractiveSearchSource(call, *lease) }()
			defer func() {
				cancel()
				release.Do(func() { close(client.release) })
				if !joined {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("physical call did not join")
					}
				}
			}()
			select {
			case <-client.entered:
			case err = <-done:
				joined = true
				t.Fatalf("call failed before entering injected I/O: %v", err)
			case <-ctx.Done():
				t.Fatal("source did not enter")
			}
			switch scenario {
			case "cancel":
				if _, err = repo.CancelInteractiveSearch(ctx, actor, first.ID, "cancel-physical-search", interactiveOtherHash); err != nil {
					t.Fatal("cancel", err)
				}
				cancel()
			case "lease-loss":
				if _, err = p.Exec(ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, task.Lease.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = repo.ExpireInteractiveSearches(ctx, 64); err != nil {
					t.Fatal("expire original lease", err)
				}
			default:
				select {
				case <-client.cancelled:
				case <-ctx.Done():
					t.Fatal("network context deadline was not delivered")
				}
			}
			select {
			case <-done:
				joined = true
				t.Fatal("logical terminal state detached real I/O")
			default:
			}
			var running int
			expectedRunning := 0
			if scenario == "network-timeout" {
				expectedRunning = 1
			}
			if err = p.QueryRow(ctx, `SELECT count(*) FROM indexer_query WHERE indexer_id=$1 AND state='running'`, site).Scan(&running); err != nil || running != expectedRunning {
				t.Fatalf("logical query terminal: %d %v", running, err)
			}
			if unlock, e := postgres.AcquireIndexerPermit(ctx, p, site); e == nil {
				unlock()
				t.Fatal("physical permit released before call returned")
			}
			second := interactiveCreate(t, repo, actor, "second-physical-search")
			nextTask := interactiveClaim(t, p, second, site)
			nextLease, err := repo.LoadInteractiveSearchSource(ctx, nextTask)
			if err != nil {
				t.Fatal("load second demand", err)
			}
			independent := search.NewService(postgres.NewSearchExecutionStore(p), interactiveIDs{}, codec, 0)
			independent.RegisterClient(site, client)
			if err = independent.ExecuteInteractiveSearchSource(ctx, *nextLease); !errors.Is(err, ports.ErrInteractiveSearchDeferred) {
				t.Fatalf("second instance must defer: %v", err)
			}
			if client.calls.Load() != 1 {
				t.Fatal("second instance performed external I/O")
			}
			release.Do(func() { close(client.release) })
			select {
			case err = <-done:
				joined = true
				if err == nil {
					t.Fatal("old callback succeeded after losing authority")
				}
				if scenario == "network-timeout" && !errors.Is(err, ports.ErrTimeout) {
					t.Fatalf("late success must be classified as timeout: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("original physical call did not return")
			}
			var candidates, successes int
			if err = p.QueryRow(ctx, `SELECT count(*) FROM release_candidate WHERE indexer_id=$1`, site).Scan(&candidates); err != nil {
				t.Fatal(err)
			}
			if err = p.QueryRow(ctx, `SELECT count(*) FROM indexer_query WHERE indexer_id=$1 AND state='succeeded'`, site).Scan(&successes); err != nil {
				t.Fatal(err)
			}
			if candidates != 0 || successes != 0 {
				t.Fatal("old callback published candidates or query success")
			}
			if unlock, e := postgres.AcquireIndexerPermit(ctx, p, site); e != nil {
				t.Fatal("permit not released after physical return", e)
			} else {
				unlock()
			}
		})
	}
}
