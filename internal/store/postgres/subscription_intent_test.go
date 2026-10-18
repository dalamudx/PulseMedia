package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type intentFixture struct {
	pool  *pgxpool.Pool
	store *postgres.SubscriptionIntentStore
	svc   *subscription.Service
	actor *ports.Actor
	root  uuid.UUID
}

func newIntentFixture(t *testing.T) intentFixture {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, e := postgres.Migrate(ctx, dsn, "../../../migrations"); e != nil {
		t.Fatal(e)
	}
	pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	ids := idgen.New()
	store := postgres.NewSubscriptionIntentStore(pool, ids)
	q := db.New(pool)
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	if _, e = q.CreateUser(ctx, db.CreateUserParams{ID: uuid.MustParse(actor.ID), Username: "intent-owner", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	root := uuid.New()
	if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Name: "TV", Path: "/intent-fixture", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	return intentFixture{pool: pool, store: store, svc: subscription.NewService(store, store, postgres.NewSubscriptionControlStore(pool, ids), store), actor: actor, root: root}
}
func (f intentFixture) command() ports.CreateSubscriptionCommand {
	return ports.CreateSubscriptionCommand{RootID: f.root, ScopeType: "all", MediaInput: &ports.SubscriptionMediaInput{Namespace: "tmdb.tv", ExternalID: "100", Title: "Intent Fixture", MediaType: "tv", Seasons: []ports.SeasonEpisodes{{Season: 0, Count: 1}, {Season: 1, Count: 2}, {Season: 2, Count: 1}}}}
}
func execIntent(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, e := pool.Exec(context.Background(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func intentCount(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if e := pool.QueryRow(context.Background(), sql, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func TestSubscriptionIntentAtomicMediaPublicationRollback(t *testing.T) {
	f := newIntentFixture(t)
	ctx := context.Background()
	q := db.New(f.pool)
	channel := uuid.New()
	if _, e := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: channel, Name: "intent-channel", ChannelType: "webhook", ConfigEncrypted: []byte("fixture-ciphertext")}); e != nil {
		t.Fatal(e)
	}
	execIntent(t, f.pool, `CREATE FUNCTION fail_intent_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'intent audit fault'; END $$`)
	execIntent(t, f.pool, `CREATE TRIGGER intent_audit_fault BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_intent_audit()`)
	if _, _, e := f.svc.CreateSubscription(ctx, f.actor, f.command()); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatalf("late failure: %v", e)
	}
	for _, table := range []string{"media", "media_identity", "media_unit", "subscription", "wanted_item", "outbox_event", "outbox_delivery", "audit_log"} {
		if n := intentCount(t, f.pool, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatalf("%s leaked %d rows", table, n)
		}
	}
	execIntent(t, f.pool, `DROP TRIGGER intent_audit_fault ON audit_log`)
	sub, rows, e := f.svc.CreateSubscription(ctx, f.actor, f.command())
	if e != nil || len(rows) != 3 || sub.State != "active" {
		t.Fatalf("create: %+v %d %v", sub, len(rows), e)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM outbox_delivery WHERE consumer=$1", "notify:"+channel.String()); n != 1 {
		t.Fatalf("recipient snapshot: %d", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM outbox_event WHERE aggregate_id=$1 AND seq=1 AND event_type='subscription.created'", sub.ID.String()); n != 1 {
		t.Fatalf("event format: %d", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM idempotency_record"); n != 0 {
		t.Fatalf("invented receipts: %d", n)
	}
}

func TestSubscriptionIntentReconfigurePreservesStateScopeAndHistory(t *testing.T) {
	for _, state := range []string{"paused", "archived"} {
		t.Run(state, func(t *testing.T) {
			f := newIntentFixture(t)
			ctx := context.Background()
			sub, rows, e := f.svc.CreateSubscription(ctx, f.actor, f.command())
			if e != nil {
				t.Fatal(e)
			}
			first := rows[0]
			if e = f.svc.SetWantedSkipped(ctx, f.actor, sub.ID, first.UnitID, true); e != nil {
				t.Fatal(e)
			}
			_, e = f.svc.UpdateState(ctx, f.actor, sub.ID, state)
			if e != nil {
				t.Fatal(e)
			}
			c := f.command()
			c.ScopeType = "selected"
			c.SelectedUnitIDs = []uuid.UUID{first.UnitID}
			sub, _, e = f.svc.CreateSubscription(ctx, f.actor, c)
			if e != nil || sub.State != state {
				t.Fatalf("narrow: %+v %v", sub, e)
			}
			// Clearing a user skip must retain exclusion from the selected scope.
			var excluded uuid.UUID
			for _, w := range rows {
				if w.UnitID != first.UnitID {
					excluded = w.UnitID
					break
				}
			}
			if e = f.svc.SetWantedSkipped(ctx, f.actor, sub.ID, excluded, false); e != nil {
				t.Fatal(e)
			}
			if n := intentCount(t, f.pool, "SELECT count(*) FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2 AND scope_excluded AND is_skipped AND NOT user_skipped", sub.ID, excluded); n != 1 {
				t.Fatal("scope exclusion erased")
			}
			before := sub.IntentVersion
			sub, rows, e = f.svc.CreateSubscription(ctx, f.actor, f.command())
			if e != nil || sub.State != state || sub.IntentVersion <= before {
				t.Fatalf("expand: %+v %v", sub, e)
			}
			if len(rows) != 3 {
				t.Fatalf("scope rows: %d", len(rows))
			}
			for _, w := range rows {
				if w.UnitID == first.UnitID && (w.ID != first.ID || !w.UserSkipped || !w.IsSkipped || w.ScopeExcluded) {
					t.Fatalf("lost provenance/history: %+v", w)
				}
				if w.UnitID != first.UnitID && (w.UserSkipped || w.ScopeExcluded || w.IsSkipped) {
					t.Fatalf("failed re-expansion: %+v", w)
				}
			}
			if n := intentCount(t, f.pool, "SELECT count(*) FROM wanted_item WHERE subscription_id=$1", sub.ID); n != 3 {
				t.Fatalf("history duplicated: %d", n)
			}
		})
	}
}

func TestSubscriptionIntentOwnedMutationPublicationRollback(t *testing.T) {
	f := newIntentFixture(t)
	ctx := context.Background()
	sub, rows, e := f.svc.CreateSubscription(ctx, f.actor, f.command())
	if e != nil {
		t.Fatal(e)
	}
	execIntent(t, f.pool, `CREATE FUNCTION fail_intent_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'intent event fault'; END $$`)
	execIntent(t, f.pool, `CREATE TRIGGER intent_event_fault BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION fail_intent_event()`)
	if _, e = f.svc.UpdateState(ctx, f.actor, sub.ID, "paused"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatalf("state failure: %v", e)
	}
	if e = f.svc.SetWantedSkipped(ctx, f.actor, sub.ID, rows[0].UnitID, true); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatalf("skip failure: %v", e)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM subscription WHERE id=$1 AND state='active' AND intent_version=1", sub.ID); n != 1 {
		t.Fatal("state/version escaped rollback")
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM wanted_item WHERE subscription_id=$1 AND (user_skipped OR is_skipped OR intent_version<>1)", sub.ID); n != 0 {
		t.Fatal("wanted escaped rollback")
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM audit_log"); n != 1 {
		t.Fatal("success audit escaped rollback")
	}
	execIntent(t, f.pool, `DROP TRIGGER intent_event_fault ON outbox_event`)
	if _, e = f.svc.UpdateState(ctx, f.actor, sub.ID, "paused"); e != nil {
		t.Fatal(e)
	}
	if e = f.svc.SetWantedSkipped(ctx, f.actor, sub.ID, rows[0].UnitID, true); e != nil {
		t.Fatal(e)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM outbox_event WHERE aggregate_id=$1 AND seq IN (1,2,3)", sub.ID.String()); n != 3 {
		t.Fatal("rollback consumed event sequence")
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM audit_log"); n != 3 {
		t.Fatal("missing success audit")
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM idempotency_record"); n != 0 {
		t.Fatal("legacy command invented idempotency keys")
	}
}

func TestSubscriptionIntentCurrentAccountAndObjectAuthorization(t *testing.T) {
	f := newIntentFixture(t)
	ctx := context.Background()
	sub, rows, e := f.svc.CreateSubscription(ctx, f.actor, f.command())
	if e != nil {
		t.Fatal(e)
	}
	foreign := *f.actor
	foreign.ID = uuid.NewString()
	if _, e = db.New(f.pool).CreateUser(ctx, db.CreateUserParams{ID: uuid.MustParse(foreign.ID), Username: "foreign", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	if e = f.store.Within(ctx, func(tx ports.SubscriptionIntentTx) error {
		_, e := tx.LockCreate(ctx, ports.SubscriptionCreateRequest{Actor: *f.actor, OwnerID: uuid.MustParse(foreign.ID), Command: f.command()})
		return e
	}); !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("frozen create owner revalidation: %v", e)
	}
	if _, e = f.svc.UpdateState(ctx, &foreign, sub.ID, "paused"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal(e)
	}
	if e = f.svc.SetWantedSkipped(ctx, &foreign, sub.ID, rows[0].UnitID, true); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal(e)
	}
	if _, _, e = f.store.ReadSubscriptionDetail(ctx, &foreign, sub.ID); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal(e)
	}
	for _, mutation := range []string{"is_active=false", "role='admin'"} {
		execIntent(t, f.pool, "UPDATE app_user SET "+mutation+" WHERE id=$1", uuid.MustParse(f.actor.ID))
		if _, _, e = f.svc.CreateSubscription(ctx, f.actor, f.command()); !errors.Is(e, ports.ErrForbidden) {
			t.Fatalf("create stale account: %v", e)
		}
		if _, e = f.svc.UpdateState(ctx, f.actor, sub.ID, "paused"); !errors.Is(e, ports.ErrForbidden) {
			t.Fatalf("state stale account: %v", e)
		}
		if e = f.svc.SetWantedSkipped(ctx, f.actor, sub.ID, rows[0].UnitID, true); !errors.Is(e, ports.ErrForbidden) {
			t.Fatalf("skip stale account: %v", e)
		}
		if _, _, e = f.store.ReadSubscriptionDetail(ctx, f.actor, sub.ID); !errors.Is(e, ports.ErrForbidden) {
			t.Fatalf("detail stale account: %v", e)
		}
		execIntent(t, f.pool, "UPDATE app_user SET is_active=true,role='member' WHERE id=$1", uuid.MustParse(f.actor.ID))
	}
	token := *f.actor
	token.Scopes = []string{"read"}
	if _, e = f.svc.UpdateState(ctx, &token, sub.ID, "paused"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal(e)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM subscription WHERE id=$1 AND state='active' AND intent_version=1", sub.ID); n != 1 {
		t.Fatal("rejected mutations wrote state")
	}
}

func TestSubscriptionIntentConcurrentReconfigureAndCurrentAccountLock(t *testing.T) {
	f := newIntentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	ids := make(chan uuid.UUID, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub, _, e := f.svc.CreateSubscription(ctx, f.actor, f.command())
			errs <- e
			if e == nil {
				ids <- sub.ID
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var subID uuid.UUID
	for id := range ids {
		if subID != uuid.Nil && subID != id {
			t.Fatal("duplicate subscription")
		}
		subID = id
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM subscription WHERE id=$1 AND intent_version=8", subID); n != 1 {
		t.Fatal("lost concurrent intent")
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM media_identity"); n != 1 {
		t.Fatal("identity fork")
	}
	if n := intentCount(t, f.pool, "SELECT count(DISTINCT seq) FROM outbox_event WHERE aggregate_id=$1", subID.String()); n != 8 {
		t.Fatalf("sequence collision: %d", n)
	}
	// The state command must wait for the account row and see the committed disable.
	blocker, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, e = blocker.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID)); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() { close(started); _, e := f.svc.UpdateState(ctx, f.actor, subID, "paused"); result <- e }()
	<-started
	if e = blocker.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("current-account lock: %v", e)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM subscription WHERE id=$1 AND state='active' AND intent_version=8", subID); n != 1 {
		t.Fatal("stale authority wrote")
	}
}

func TestSubscriptionIntentRejectsUnknownScopeAndRetiredUnits(t *testing.T) {
	f := newIntentFixture(t)
	ctx := context.Background()
	sub, rows, e := f.svc.CreateSubscription(ctx, f.actor, f.command())
	if e != nil {
		t.Fatal(e)
	}
	cases := []ports.CreateSubscriptionCommand{}
	unknown := f.command()
	unknown.ScopeType = "selected"
	unknown.SelectedUnitIDs = []uuid.UUID{uuid.New()}
	cases = append(cases, unknown)
	absent := f.command()
	absent.ScopeType = "selected"
	absent.EpisodeKeys = []string{"S01E99"}
	cases = append(cases, absent)
	for i, c := range cases {
		if _, _, e = f.svc.CreateSubscription(ctx, f.actor, c); !errors.Is(e, subscription.ErrInvalidScope) {
			t.Fatalf("case %d: %v", i, e)
		}
	}
	execIntent(t, f.pool, "UPDATE media_unit SET retired=true WHERE id=$1", rows[0].UnitID)
	selected := f.command()
	selected.ScopeType = "selected"
	selected.SelectedUnitIDs = []uuid.UUID{rows[0].UnitID}
	if _, _, e = f.svc.CreateSubscription(ctx, f.actor, selected); !errors.Is(e, subscription.ErrInvalidScope) {
		t.Fatal(e)
	}
	if _, _, e = f.svc.CreateSubscription(ctx, f.actor, f.command()); e != nil {
		t.Fatal(e)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2 AND scope_excluded AND is_skipped", sub.ID, rows[0].UnitID); n != 1 {
		t.Fatal("retired history changed")
	}
	if _, _, e = f.svc.CreateSubscription(ctx, f.actor, ports.CreateSubscriptionCommand{RootID: f.root, MediaInput: &ports.SubscriptionMediaInput{Namespace: "tmdb.tv", ExternalID: "missing", Title: "No Episodes", MediaType: "tv"}}); !errors.Is(e, subscription.ErrInvalidScope) {
		t.Fatalf("TV without trusted units: %v", e)
	}
}
