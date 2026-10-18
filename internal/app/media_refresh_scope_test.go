package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/google/uuid"
	"sync/atomic"
	"testing"
	"time"
)

type mediaRefreshProbe struct {
	calls         atomic.Int32
	enter, resume chan struct{}
	run           func(context.Context) error
}

func (p *mediaRefreshProbe) RefreshPaths(ctx context.Context, _ []string) error {
	p.calls.Add(1)
	if p.enter != nil {
		close(p.enter)
		<-p.resume
	}
	if p.run != nil {
		return p.run(ctx)
	}
	return nil
}
func (p *mediaRefreshProbe) FindByPath(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (p *mediaRefreshProbe) Test(context.Context) error { return nil }
func refreshFixture(t *testing.T) (*organizationFixture, *postgres.MediaConfirmationStore, *ports.MediaConfirmationInput, *outbox.Outbox, *db.OutboxDelivery) {
	t.Helper()
	f, store, in := confirmationFixture(t)
	ob := outbox.New(f.pool, f.gen, clock.New())
	tx, e := f.pool.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = ob.PublishImported(context.Background(), db.New(tx), in.UnitID.String(), map[string]any{"unit_id": in.UnitID, "file_id": in.FileID}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
	d, e := ob.Claim(context.Background(), "mediaserver.refresh:"+in.Server.ID.String(), "first", time.Minute)
	if e != nil || d == nil {
		t.Fatal("refresh not queued", e)
	}
	return f, store, in, ob, d
}
func TestMediaRefreshIgnoresLogicalExpiryUntilHTTPReturns(t *testing.T) {
	f, store, in, ob, d := refreshFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &mediaRefreshProbe{enter: make(chan struct{}), resume: make(chan struct{})}
	svc := mediaserver.NewSyncService(ob, store)
	svc.RegisterClient(in.Server.ID, client)
	done := make(chan error, 1)
	go func() { done <- svc.ProcessRefreshDelivery(ctx, d.ID, in.Server.ID, "first", d.LeaseToken, "") }()
	joined := false
	defer func() {
		if !joined {
			select {
			case <-client.resume:
			default:
				close(client.resume)
			}
			<-done
		}
	}()
	select {
	case <-client.enter:
	case <-time.After(5 * time.Second):
		t.Fatal("firstrefresh not entered")
	}
	cancel()
	if _, e := f.pool.Exec(context.Background(), `UPDATE outbox_delivery SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, d.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.q.RecoverExpiredOutboxDeliveries(context.Background(), d.Consumer); e != nil {
		t.Fatal(e)
	}
	next, e := ob.Claim(context.Background(), d.Consumer, "second", time.Minute)
	if e != nil || next == nil {
		t.Fatal(e)
	}
	second := mediaserver.NewSyncService(ob, postgres.NewMediaConfirmationRepository(f.pool, f.gen))
	second.RegisterClient(in.Server.ID, client)
	if e = second.ProcessRefreshDelivery(context.Background(), next.ID, in.Server.ID, "second", next.LeaseToken, ""); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("logical expiry freed physicalsend", e)
	}
	server2 := uuid.New()
	release, e := store.AcquireMediaRefreshPermit(context.Background(), server2)
	if e != nil {
		t.Fatal("differentserver blocked", e)
	}
	release()
	release()
	if client.calls.Load() != 1 {
		t.Fatal("duplicateHTTP before return")
	}
	close(client.resume)
	e = <-done
	joined = true
	if e == nil {
		t.Fatal("expiredfirstowner finished delivery")
	}
	fresh := &mediaRefreshProbe{}
	second.RegisterClient(in.Server.ID, fresh)
	if e = second.ProcessRefreshDelivery(context.Background(), next.ID, in.Server.ID, "second", next.LeaseToken, ""); e != nil {
		t.Fatal("physicalreturn didnot allow recoveredowner", e)
	}
	row, e := f.q.GetOutboxDeliveryByID(context.Background(), d.ID)
	if e != nil || row.State != "delivered" || fresh.calls.Load() != 1 {
		t.Fatal("recovered refresh not complete", e)
	}
	facts, e := f.q.ListActiveFactsForUnit(context.Background(), in.UnitID)
	if e != nil || len(facts) != 1 || facts[0].Stage != "imported" {
		t.Fatal("refresh generated confirmation", e)
	}
}
func TestMediaRefreshRejectsLateConfigurationSuccess(t *testing.T) {
	f, store, in, ob, d := refreshFixture(t)
	client := &mediaRefreshProbe{run: func(ctx context.Context) error {
		_, e := f.pool.Exec(ctx, `UPDATE media_server SET url='https://replacement.invalid',updated_at=clock_timestamp() WHERE id=$1`, in.Server.ID)
		return e
	}}
	svc := mediaserver.NewSyncService(ob, store)
	svc.RegisterClient(in.Server.ID, client)
	if e := svc.ProcessRefreshDelivery(context.Background(), d.ID, in.Server.ID, "first", d.LeaseToken, ""); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("lateoldconfig success accepted", e)
	}
	row, e := f.q.GetOutboxDeliveryByID(context.Background(), d.ID)
	if e != nil || row.State != "delivering" || client.calls.Load() != 1 {
		t.Fatal("late response completed originaldelivery", e)
	}
	if e = ob.Fail(context.Background(), d.ID, "first", d.LeaseToken, "configuration changed", time.Millisecond); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(context.Background(), `UPDATE outbox_delivery SET not_before=clock_timestamp() WHERE id=$1`, d.ID); e != nil {
		t.Fatal(e)
	}
	next, e := ob.Claim(context.Background(), d.Consumer, "new-config", time.Minute)
	if e != nil || next == nil {
		t.Fatal(e)
	}
	svc.RegisterClient(in.Server.ID, &mediaRefreshProbe{})
	if e = svc.ProcessRefreshDelivery(context.Background(), next.ID, in.Server.ID, "new-config", next.LeaseToken, ""); e != nil {
		t.Fatal("currentconfig retry failed", e)
	}
	facts, e := f.q.ListActiveFactsForUnit(context.Background(), in.UnitID)
	if e != nil || len(facts) != 1 || facts[0].Stage != "imported" {
		t.Fatal("refresh removed imported", e)
	}
}
func TestMediaRefreshRejectsChangedFileScope(t *testing.T) {
	f, store, in, _, d := refreshFixture(t)
	ctx := context.Background()
	input, e := store.LoadMediaRefresh(ctx, d.ID, in.Server.ID, "first", d.LeaseToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, `UPDATE library_file SET mtime_ns=mtime_ns+1 WHERE id=$1`, in.FileID); e != nil {
		t.Fatal(e)
	}
	if e = store.ValidateMediaRefresh(ctx, *input); !errors.Is(e, ports.ErrMediaFileRetired) {
		t.Fatal("stalesend scope accepted", e)
	}
	if e = store.FinishMediaRefresh(ctx, *input); !errors.Is(e, ports.ErrMediaFileRetired) {
		t.Fatal("stalecompletion accepted", e)
	}
	row, e := f.q.GetOutboxDeliveryByID(ctx, d.ID)
	if e != nil || row.State != "delivering" {
		t.Fatal("staleresult completed", e)
	}
}

func TestMediaConfirmationAndRefreshRejectBorrowedStamp(t *testing.T) {
	f, s, in, _, d := refreshFixture(t)
	ctx := context.Background()
	refresh, e := s.LoadMediaRefresh(ctx, d.ID, in.Server.ID, "first", d.LeaseToken)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		name   string
		change func(*ports.MediaConfirmationInput)
	}{{"remote-path", func(x *ports.MediaConfirmationInput) { x.RemotePath = "/other/file.mkv" }}, {"connection-url", func(x *ports.MediaConfirmationInput) { x.Server.URL = "https://other.invalid" }}, {"connection-type", func(x *ports.MediaConfirmationInput) { x.Server.Kind = "plex" }}, {"connection-key", func(x *ports.MediaConfirmationInput) { x.Server.EncryptedKey = []byte("other-connection") }}} {
		t.Run(c.name, func(t *testing.T) {
			before := confirmationState(t, f, in)
			borrowed := *in
			c.change(&borrowed)
			if _, e = s.CommitMediaConfirmation(ctx, borrowed, "native-item"); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal("borrowed stamp confirmedfile", e)
			}
			copy := *refresh
			copy.Confirmation = borrowed
			if e = s.ValidateMediaRefresh(ctx, copy); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal("borrowed stamp permittedrefresh", e)
			}
			if e = s.FinishMediaRefresh(ctx, copy); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal("borrowed stamp finishedrefresh", e)
			}
			if confirmationState(t, f, in) != before {
				t.Fatal("borrowed connection changedfilefacts")
			}
			row, e := f.q.GetOutboxDeliveryByID(ctx, d.ID)
			if e != nil || row.State != "delivering" {
				t.Fatal("borrowedconnection completed delivery", e)
			}
		})
	}
}

func TestMediaRefreshRejectsBorrowedDelivery(t *testing.T) {
	f, s, in, ob, first := refreshFixture(t)
	ctx := context.Background()
	input, e := s.LoadMediaRefresh(ctx, first.ID, in.Server.ID, "first", first.LeaseToken)
	if e != nil {
		t.Fatal(e)
	}
	other := uuid.New()
	event, e := f.q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: uuid.New(), AggregateType: "media_unit", AggregateID: other.String(), EventType: "media.imported", Payload: []byte(`{}`), Seq: 1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: uuid.New(), EventID: event.ID, Consumer: first.Consumer, AggregateType: event.AggregateType, AggregateID: other.String(), Seq: 1, MaxAttempts: 5}); e != nil {
		t.Fatal(e)
	}
	next, e := ob.Claim(ctx, first.Consumer, "first", time.Minute)
	if e != nil || next == nil {
		t.Fatal(e)
	}
	input.DeliveryID = next.ID
	input.Token = next.LeaseToken
	before := confirmationState(t, f, in)
	if e = s.ValidateMediaRefresh(ctx, *input); !errors.Is(e, ports.ErrBadResponse) {
		t.Fatal("foreign event scope permitted", e)
	}
	if e = s.FinishMediaRefresh(ctx, *input); !errors.Is(e, ports.ErrBadResponse) {
		t.Fatal("foreign event scope completed", e)
	}
	for _, id := range []uuid.UUID{first.ID, next.ID} {
		row, e := f.q.GetOutboxDeliveryByID(ctx, id)
		if e != nil || row.State != "delivering" {
			t.Fatal("scope borrowing changeddelivery", e)
		}
	}
	if confirmationState(t, f, in) != before {
		t.Fatal("scope borrowing changedfile")
	}
}
