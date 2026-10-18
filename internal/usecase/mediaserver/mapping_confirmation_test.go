package mediaserver_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/google/uuid"
	"testing"
	"time"
)

type delayedMappingConfirmation struct {
	mockMediaServerClient
	entered chan struct{}
	resume  chan struct{}
}

func (c *delayedMappingConfirmation) FindByPath(ctx context.Context, _ string) (string, bool, error) {
	close(c.entered)
	select {
	case <-c.resume:
		return "observed-before-correction", true, nil
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
}
func TestLateConfirmationCannotReviveRetiredMapping(t *testing.T) {
	svc, _, q, _, unit, file, server := setupSyncTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	client := &delayedMappingConfirmation{entered: make(chan struct{}), resume: make(chan struct{})}
	svc.RegisterClient(server, client)
	done := make(chan error, 1)
	go func() { _, e := svc.ConfirmFile(ctx, server, file, unit); done <- e }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	select {
	case <-client.entered:
	case <-ctx.Done():
		t.Fatal("confirmation did not reach external probe")
	}
	if e := q.RetireFileMappingsExcept(ctx, db.RetireFileMappingsExceptParams{FileID: file, Column2: []uuid.UUID{}}); e != nil {
		t.Fatal(e)
	}
	close(client.resume)
	e := <-done
	joined = true
	if !errors.Is(e, mediaserver.ErrFileRetired) {
		t.Fatal("stale confirmation returned success", e)
	}
	facts, e := q.ListActiveFactsForUnit(ctx, unit)
	if e != nil {
		t.Fatal(e)
	}
	for _, fact := range facts {
		if fact.Stage == "confirmed" {
			t.Fatal("late external response manufactured current confirmed fact")
		}
	}
}
