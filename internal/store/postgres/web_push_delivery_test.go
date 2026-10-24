package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type pushDeliveryFixture struct {
	pushFixture
	ob                                   *outbox.Outbox
	device                               *ports.WebPushDevice
	unit, file, root, subscription, fact uuid.UUID
}

func newPushDeliveryFixture(t *testing.T) pushDeliveryFixture {
	t.Helper()
	f := newPushFixture(t)
	if _, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), repositoryMigrationsThrough(t, 121)); err != nil {
		t.Fatal(err)
	}
	device := f.register(t, "device")
	q := db.New(f.p)
	media, err := q.CreateMedia(t.Context(), db.CreateMediaParams{ID: uuid.New(), Title: "Owned push media", MediaType: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	unit, err := q.CreateMediaUnit(t.Context(), db.CreateMediaUnitParams{ID: uuid.New(), MediaID: media.ID, UnitKey: "S01E07"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := q.CreateLibraryRoot(t.Context(), db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "tv", Name: "Owned"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := q.CreateLibraryFile(t.Context(), db.CreateLibraryFileParams{ID: uuid.New(), RootID: root.ID, RelPath: "Owned.mkv", SizeBytes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.LinkFileUnit(t.Context(), db.LinkFileUnitParams{FileID: file.ID, UnitID: unit.ID}); err != nil {
		t.Fatal(err)
	}
	fact, err := q.CreateUnitFact(t.Context(), db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit.ID, Stage: "imported", RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p"}`)})
	if err != nil {
		t.Fatal(err)
	}
	subscription := uuid.New()
	f.exec(t, `INSERT INTO subscription(id,user_id,media_id,root_id) VALUES($1,$2,$3,$4)`, subscription, f.actor.ID, media.ID, root.ID)
	f.exec(t, `INSERT INTO wanted_item(id,subscription_id,unit_id,intent_version) VALUES($1,$2,$3,1)`, uuid.New(), subscription, unit.ID)
	ob := outbox.New(f.p, idgen.New(), clock.New())
	ob.EnableWebPush(f.config.Version)
	return pushDeliveryFixture{f, ob, device, unit.ID, file.ID, root.ID, subscription, fact.ID}
}
func (f pushDeliveryFixture) publish(t *testing.T) *db.OutboxEvent {
	t.Helper()
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	event, err := f.ob.PublishImported(t.Context(), db.New(tx), f.unit.String(), map[string]any{"unit_id": f.unit, "file_id": f.file})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return event
}
func (f pushDeliveryFixture) claim(t *testing.T) *db.OutboxDelivery {
	t.Helper()
	d, err := f.ob.Claim(t.Context(), "webpush:"+f.device.ID.String(), "owned-worker", time.Minute)
	if err != nil || d == nil {
		t.Fatal("missing delivery", err)
	}
	return d
}
func (f pushDeliveryFixture) execution(t *testing.T) ports.WebPushDeliveryExecution {
	t.Helper()
	e, err := postgres.NewWebPushDeliveryStore(f.p).OpenWebPushExecution(t.Context(), f.device.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Error(err)
		}
	})
	return e
}
func TestWebPushDeliveryFrozenRecipientsAndRollbackPG(t *testing.T) {
	f := newPushDeliveryFixture(t)
	admin := pgtest.CreateUserActor(t, db.New(f.p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	adminSub := f.sub
	adminSub.Endpoint += "-admin"
	if _, err := f.service.Register(t.Context(), admin, uuid.New(), f.config.Version, adminSub, "admin"); err != nil {
		t.Fatal(err)
	}
	other := pgtest.CreateUserActor(t, db.New(f.p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	otherSub := f.sub
	otherSub.Endpoint += "-unrelated"
	if _, err := f.service.Register(t.Context(), other, uuid.New(), f.config.Version, otherSub, "other"); err != nil {
		t.Fatal(err)
	}
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.ob.PublishImported(t.Context(), db.New(tx), f.unit.String(), map[string]any{"unit_id": f.unit, "file_id": f.file}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.p.QueryRow(t.Context(), `SELECT count(*) FROM web_push_delivery_binding`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rolled back recipient survived", err)
	}
	event := f.publish(t)
	if err = f.p.QueryRow(t.Context(), `SELECT count(*) FROM web_push_delivery_binding b JOIN outbox_delivery d ON d.id=b.delivery_id WHERE d.event_id=$1`, event.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("recipient visibility mismatch", count, err)
	}
	late := f.sub
	late.Endpoint += "-late"
	if _, err = f.service.Register(t.Context(), f.actor, uuid.New(), f.config.Version, late, "late"); err != nil {
		t.Fatal(err)
	}
	if err = f.p.QueryRow(t.Context(), `SELECT count(*) FROM web_push_delivery_binding`).Scan(&count); err != nil || count != 2 {
		t.Fatal("late registration rewrote history", err)
	}
	if _, err = f.p.Exec(t.Context(), `UPDATE web_push_delivery_binding SET user_id=$1`, other.ID); err == nil {
		t.Fatal("recipient could be retargeted")
	}
}
func TestWebPushDeliveryLeaseAndPhysicalLockPG(t *testing.T) {
	f := newPushDeliveryFixture(t)
	event := f.publish(t)
	d := f.claim(t)
	e := f.execution(t)
	if _, err := postgres.NewWebPushDeliveryStore(f.p).OpenWebPushExecution(t.Context(), f.device.ID); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("overlapping physical execution", err)
	}
	owned, err := e.Load(t.Context(), d.ID, "owned-worker", d.LeaseToken, f.config.Version)
	if err != nil || owned.EventID != event.ID || len(owned.Ciphertext) < 40 {
		t.Fatal("binding load", err)
	}
	if _, err = e.Load(t.Context(), d.ID, "other-owner", d.LeaseToken, f.config.Version); !errors.Is(err, ports.ErrStaleDeliveryLease) {
		t.Fatal("wrong owner allowed", err)
	}
	f.exec(t, `UPDATE outbox_delivery SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, d.ID)
	if _, err = e.Load(t.Context(), d.ID, "owned-worker", d.LeaseToken, f.config.Version); !errors.Is(err, ports.ErrStaleDeliveryLease) {
		t.Fatal("late load", err)
	}
	if err = e.Complete(t.Context(), d.ID, "owned-worker", d.LeaseToken, "accepted"); !errors.Is(err, ports.ErrStaleDeliveryLease) {
		t.Fatal("late success committed", err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := postgres.NewWebPushDeliveryStore(f.p).OpenWebPushExecution(t.Context(), f.device.ID)
	if err != nil {
		t.Fatal("physical release not effective", err)
	}
	if err = next.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestWebPushDeliveryCurrentPermissionAndVersionPG(t *testing.T) {
	for _, scenario := range []string{"revoked", "inactive", "archived", "fact_revoked", "file_retired", "vapid"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPushDeliveryFixture(t)
			f.publish(t)
			d := f.claim(t)
			e := f.execution(t)
			version := f.config.Version
			switch scenario {
			case "revoked":
				if _, err := f.service.Revoke(t.Context(), f.actor, f.device.ID, f.device.UpdatedAt, "revoke"); err != nil {
					t.Fatal(err)
				}
			case "inactive":
				f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID)
			case "archived":
				f.exec(t, `UPDATE subscription SET state='archived' WHERE id=$1`, f.subscription)
			case "fact_revoked":
				f.exec(t, `UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE id=$1`, f.fact)
			case "file_retired":
				f.exec(t, `UPDATE library_file SET retired_at=clock_timestamp() WHERE id=$1`, f.file)
			case "vapid":
				version = strings.Repeat("a", 64)
			}
			if _, err := e.Load(t.Context(), d.ID, "owned-worker", d.LeaseToken, version); !errors.Is(err, ports.ErrForbidden) {
				t.Fatal("stale permission allowed", err)
			}
			if err := e.Complete(t.Context(), d.ID, "owned-worker", d.LeaseToken, "skipped"); err != nil {
				t.Fatal(err)
			}
			stored, err := db.New(f.p).GetOutboxDeliveryByID(t.Context(), d.ID)
			if err != nil || stored.State != "skipped" || stored.DeliveredAt.Valid {
				t.Fatal("no-send marked as delivered", err)
			}
		})
	}
}
func TestWebPushDeliveryExpiryAtomicRollbackPG(t *testing.T) {
	f := newPushDeliveryFixture(t)
	f.publish(t)
	d := f.claim(t)
	e := f.execution(t)
	f.exec(t, `CREATE FUNCTION push_completion_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private endpoint'; END $$; CREATE TRIGGER push_completion_fault BEFORE UPDATE ON outbox_delivery FOR EACH ROW EXECUTE FUNCTION push_completion_fault()`)
	if err := e.Complete(t.Context(), d.ID, "owned-worker", d.LeaseToken, "expired"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("tail fault", err)
	}
	var status string
	if err := f.p.QueryRow(t.Context(), `SELECT status FROM web_push_device WHERE id=$1`, f.device.ID).Scan(&status); err != nil || status != "active" {
		t.Fatal("partial expiry", err)
	}
	f.exec(t, `DROP TRIGGER push_completion_fault ON outbox_delivery; DROP FUNCTION push_completion_fault()`)
	if err := e.Complete(t.Context(), d.ID, "owned-worker", d.LeaseToken, "expired"); err != nil {
		t.Fatal(err)
	}
	if err := f.p.QueryRow(t.Context(), `SELECT status FROM web_push_device WHERE id=$1`, f.device.ID).Scan(&status); err != nil || status != "expired" {
		t.Fatal("missing expiry", err)
	}
	stored, err := db.New(f.p).GetOutboxDeliveryByID(t.Context(), d.ID)
	if err != nil || stored.State != "skipped" {
		t.Fatal("missing expiry completion", err)
	}
}
