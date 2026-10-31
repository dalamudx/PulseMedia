package postgres_test

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestMovieDiscoveryPhysicalReturnPG(t *testing.T) {
	f, s := movieDiscoveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	before := discoveryState(t, f)
	if ok, e := s.ScheduleMovieDiscovery(ctx, f.sub); e != nil || !ok {
		t.Fatalf("schedule %v %v", ok, e)
	}
	status, e := s.ReadMovieDiscovery(ctx, f.actor, f.sub)
	if e != nil || status.SearchID == nil {
		t.Fatal(status, e)
	}
	repo := postgres.NewInteractiveSearchStore(f.p, idgen.New())
	task := interactiveClaim(t, f.p, &ports.InteractiveSearchHeader{ID: *status.SearchID}, f.source)
	lease, e := repo.LoadInteractiveSearchSource(ctx, task)
	if e != nil {
		t.Fatal(e)
	}
	executor := usecaseSearch.NewService(postgres.NewSearchExecutionStore(f.p), idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{21}, 32)), 0)
	client := &interactiveBlockedClient{entered: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	executor.RegisterClient(f.source, client)
	call, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	var release sync.Once
	joined := false
	go func() { done <- executor.ExecuteInteractiveSearchSource(call, *lease) }()
	defer func() {
		stop()
		release.Do(func() { close(client.release) })
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("physical call not joined")
			}
		}
	}()
	select {
	case <-client.entered:
	case e := <-done:
		joined = true
		t.Fatalf("failed before physical call %v", e)
	case <-ctx.Done():
		t.Fatal("call not entered")
	}
	stop()
	select {
	case <-client.cancelled:
	case <-ctx.Done():
		t.Fatal("cancel not observed")
	}
	if unlock, e := postgres.AcquireIndexerPermit(ctx, f.p, f.source); e == nil {
		unlock()
		t.Fatal("physical lock released before native return")
	}
	select {
	case <-done:
		joined = true
		t.Fatal("cancel detached physical call")
	default:
	}
	f.exec(t, `UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1`, f.sub)
	release.Do(func() { close(client.release) })
	select {
	case e := <-done:
		joined = true
		if e == nil {
			t.Fatal("stale cancelled query succeeded")
		}
	case <-ctx.Done():
		t.Fatal("physical return not joined")
	}
	var pages int
	if e = f.p.QueryRow(ctx, `SELECT count(*) FROM interactive_search_query a JOIN indexer_query_page p ON p.query_id=a.query_id WHERE a.search_id=$1`, *status.SearchID).Scan(&pages); e != nil || pages != 0 {
		t.Fatalf("late pages %d %v", pages, e)
	}
	if after := discoveryState(t, f); after != before {
		t.Fatal("cancelled discovery created acquisition or changed file")
	}
}
