package mediaserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

type blockingRefreshClient struct {
	entered chan struct{}
	release chan struct{}
}

func (c *blockingRefreshClient) RefreshPaths(context.Context, []string) error {
	close(c.entered)
	<-c.release
	return nil
}
func (c *blockingRefreshClient) FindByPath(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (c *blockingRefreshClient) Test(context.Context) error { return nil }

func TestRefreshPermitSurvivesCancellationAndOtherServerIndependentPG(t *testing.T) {
	svc, ob, q, _, unit, file, server := setupSyncTest(t)
	ctx := context.Background()
	other := uuid.New()
	if _, err := q.CreateMediaServer(ctx, db.CreateMediaServerParams{ID: other, Name: "independent", ServerType: "jellyfin", Url: "http://localhost:8097", ApiKeyEncrypted: []byte("fixture"), PathMapping: []byte(`{"/data/media/movies":"/movies"}`)}); err != nil {
		t.Fatal(err)
	}
	event := uuid.New()
	payload, _ := json.Marshal(map[string]any{"file_id": file, "unit_id": unit})
	if _, err := q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: event, AggregateType: "media", AggregateID: unit.String(), EventType: "media.imported", Payload: payload, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	consumer := "mediaserver.refresh:" + server.String()
	if _, err := q.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: uuid.New(), EventID: event, Consumer: consumer, AggregateType: "media", AggregateID: unit.String(), Seq: 1, MaxAttempts: 5}); err != nil {
		t.Fatal(err)
	}
	claimed, err := ob.Claim(ctx, consumer, "blocking", time.Minute)
	if err != nil || claimed == nil {
		t.Fatal("claim", err)
	}
	client := &blockingRefreshClient{entered: make(chan struct{}), release: make(chan struct{})}
	svc.RegisterClient(server, client)
	ioCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(client.release) }) }
	go func() {
		done <- svc.ProcessRefreshDelivery(ioCtx, claimed.ID, server, "blocking", claimed.LeaseToken, "")
	}()
	// Always drain the task before setupSyncTest closes its pool, even on failure.
	defer func() {
		cancel()
		unblock()
		<-done
	}()
	select {
	case <-client.entered:
	case err := <-done:
		done <- err
		t.Fatal("refresh failed to enter", err)
	case <-time.After(10 * time.Second):
		t.Fatal("refresh did not enter")
	}
	cancel()
	secondCtx, stop := context.WithTimeout(ctx, 150*time.Millisecond)
	permit, err := svc.AcquireRefreshPermit(secondCtx, server)
	stop()
	if permit != nil {
		permit.Release()
		t.Fatal("cancellation released physical slot before I/O returned")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("unexpected blocked permit error", err)
	}
	otherCtx, stopOther := context.WithTimeout(ctx, time.Second)
	independent, err := svc.AcquireRefreshPermit(otherCtx, other)
	stopOther()
	if err != nil {
		t.Fatal("independent server blocked", err)
	}
	independent.Release()
	unblock()
	select {
	case <-done:
		done <- nil
	case <-time.After(10 * time.Second):
		t.Fatal("I/O did not return")
	}
	afterCtx, stopAfter := context.WithTimeout(ctx, time.Second)
	defer stopAfter()
	after, err := svc.AcquireRefreshPermit(afterCtx, server)
	if err != nil {
		t.Fatal("physical slot not released after I/O return", err)
	}
	after.Release()
}
