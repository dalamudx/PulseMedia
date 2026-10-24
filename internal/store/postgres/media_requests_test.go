package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mediaRequestFixture struct {
	pool                *pgxpool.Pool
	store               *postgres.MediaRequestStore
	owner, other, admin *ports.Actor
	dsn, history        string
	now                 time.Time
}

func newMediaRequestFixture(t *testing.T) mediaRequestFixture {
	t.Helper()
	ctx := t.Context()
	f := mediaRequestFixture{dsn: pgtest.GetCluster(t).NewTestDB(t), history: repositoryMigrationsThrough(t, 107), now: time.Date(2020, 2, 3, 4, 5, 6, 123456789, time.UTC)}
	if version, err := postgres.Migrate(ctx, f.dsn, f.history); err != nil || version != 107 {
		t.Fatal("media request migration", version, err)
	}
	pool, err := postgres.NewPool(ctx, f.dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	f.pool = pool
	t.Cleanup(pool.Close)
	f.store = postgres.NewMediaRequestStore(pool, idgen.New(), clock.NewMock(f.now))
	f.owner, f.other, f.admin = f.user(t, "owner", ports.RoleMember), f.user(t, "other", ports.RoleMember), f.user(t, "admin", ports.RoleAdmin)
	return f
}

func (f mediaRequestFixture) user(t *testing.T, name, role string) *ports.Actor {
	t.Helper()
	a := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: role}
	if _, err := db.New(f.pool).CreateUser(t.Context(), db.CreateUserParams{ID: uuid.MustParse(a.ID), Username: name, PasswordHash: "fixture", Role: role, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	return a
}

func mediaRequestIntent() mediarequest.Intent {
	return mediarequest.Intent{MediaType: "tv", TMDBID: 501, Seasons: []int{3, 1}}
}

func (f mediaRequestFixture) submit(t *testing.T, a *ports.Actor, key string) *ports.MediaRequestReceipt {
	t.Helper()
	r, err := f.store.SubmitMediaRequest(t.Context(), a, mediaRequestIntent(), key)
	if err != nil || r == nil {
		t.Fatal("submit", r, err)
	}
	return r
}

func (f mediaRequestFixture) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := f.pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f mediaRequestFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func mediaRequestWantError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func mediaRequestWantReceipt(t *testing.T, got, want *ports.MediaRequestReceipt, err error) {
	t.Helper()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("receipt = %+v, want %+v; error %v", got, want, err)
	}
}

func TestMediaRequestSubmissionConcurrentOriginalPG(t *testing.T) {
	f := newMediaRequestFixture(t)
	ctx := t.Context()
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]*ports.MediaRequestReceipt, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = f.store.SubmitMediaRequest(ctx, f.owner, mediaRequestIntent(), "same")
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0] == nil || !reflect.DeepEqual(results[0], results[1]) {
		t.Fatal("concurrent original receipts differ", results)
	}
	r := results[0]
	if r.Request.Version != 1 || r.Request.State != "pending" || r.Request.OwnerID.String() != f.owner.ID || !reflect.DeepEqual(r.Request.Intent.Seasons, []int{1, 3}) || !r.Request.CreatedAt.Equal(f.now.Truncate(time.Microsecond)) || !r.Request.UpdatedAt.Equal(r.Request.CreatedAt) {
		t.Fatalf("initial snapshot: %+v", r)
	}
	for _, query := range []string{"SELECT count(*) FROM media_request", "SELECT count(*) FROM audit_log WHERE target_type='media_request'", "SELECT count(*) FROM outbox_event WHERE aggregate_type='media_request'", "SELECT count(*) FROM idempotency_record WHERE idempotency_key='media.request.command:same'"} {
		if n := f.count(t, query); n != 1 {
			t.Fatal(query, n)
		}
	}
	// Submission grants no subscription or execution authority.
	for _, table := range []string{"subscription", "wanted_item", "job", "media", "media_unit", "library_file", "library_file_unit", "download_file_inventory"} {
		if n := f.count(t, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatal(table, n)
		}
	}
	changed := mediaRequestIntent()
	changed.TMDBID++
	_, err := f.store.SubmitMediaRequest(ctx, f.owner, changed, "same")
	mediaRequestWantError(t, err, ports.ErrMediaRequestConflict)
	other := f.submit(t, f.other, "same")
	if other.Request.ID == r.Request.ID || other.Request.OwnerID == r.Request.OwnerID {
		t.Fatal("owner-scoped command collision")
	}
	got, err := f.store.ReadMediaRequestReceipt(ctx, f.owner, "same")
	mediaRequestWantReceipt(t, got, r, err)
}

func TestMediaRequestCloseAndTerminalPG(t *testing.T) {
	f := newMediaRequestFixture(t)
	ctx := t.Context()
	reject := ports.MediaRequestCloseInput{ExpectedVersion: 1, Action: "reject", Reason: "  unavailable  "}
	withdraw := ports.MediaRequestCloseInput{ExpectedVersion: 1, Action: "withdraw"}
	r := f.submit(t, f.owner, "submit")
	_, err := f.store.CloseMediaRequest(ctx, f.owner, r.Request.ID, reject, "reject")
	mediaRequestWantError(t, err, ports.ErrForbidden)
	_, err = f.store.CloseMediaRequest(ctx, f.other, r.Request.ID, withdraw, "foreign")
	mediaRequestWantError(t, err, ports.ErrForbidden)
	_, err = f.store.CloseMediaRequest(ctx, f.admin, r.Request.ID, withdraw, "admin-withdraw")
	mediaRequestWantError(t, err, ports.ErrForbidden)
	wrong := reject
	wrong.ExpectedVersion = 2
	_, err = f.store.CloseMediaRequest(ctx, f.admin, r.Request.ID, wrong, "reject")
	mediaRequestWantError(t, err, ports.ErrMediaRequestConflict)
	closed, err := f.store.CloseMediaRequest(ctx, f.admin, r.Request.ID, reject, "reject")
	if err != nil || closed == nil {
		t.Fatal("reject", closed, err)
	}
	if closed.Request.State != "rejected" || closed.Request.Version != 2 || closed.Request.Reason != "unavailable" || closed.Request.DecidedBy == nil || closed.Request.DecidedBy.String() != f.admin.ID || closed.Request.DecidedAt == nil || closed.Request.SubscriptionID != nil {
		t.Fatalf("terminal snapshot %+v", closed)
	}
	f.exec(t, "UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid() WHERE singleton")
	got, err := f.store.SubmitMediaRequest(ctx, f.owner, mediaRequestIntent(), "submit")
	mediaRequestWantReceipt(t, got, r, err)
	got, err = f.store.ReadMediaRequestReceipt(ctx, f.owner, "submit")
	mediaRequestWantReceipt(t, got, r, err)
	got, err = f.store.CloseMediaRequest(ctx, f.admin, r.Request.ID, reject, "reject")
	mediaRequestWantReceipt(t, got, closed, err)
	got, err = f.store.ReadMediaRequestReceipt(ctx, f.admin, "reject")
	mediaRequestWantReceipt(t, got, closed, err)
	_, err = f.store.CloseMediaRequest(ctx, f.admin, r.Request.ID, reject, "another")
	mediaRequestWantError(t, err, ports.ErrMediaRequestConflict)
	_, err = f.store.SubmitMediaRequest(ctx, f.other, mediaRequestIntent(), "isolated")
	mediaRequestWantError(t, err, ports.ErrUnavailable)
	f.exec(t, "UPDATE restore_isolation SET isolated=false WHERE singleton")
	second := f.submit(t, f.owner, "second")
	_, err = f.store.CloseMediaRequest(ctx, f.owner, second.Request.ID, withdraw, "submit")
	mediaRequestWantError(t, err, ports.ErrMediaRequestConflict)
	withdrawn, err := f.store.CloseMediaRequest(ctx, f.owner, second.Request.ID, withdraw, "withdraw")
	if err != nil || withdrawn == nil || withdrawn.Request.State != "withdrawn" {
		t.Fatal(withdrawn, err)
	}
	got, err = f.store.CloseMediaRequest(ctx, f.owner, second.Request.ID, withdraw, "withdraw")
	mediaRequestWantReceipt(t, got, withdrawn, err)
	got, err = f.store.ReadMediaRequestReceipt(ctx, f.owner, "withdraw")
	mediaRequestWantReceipt(t, got, withdrawn, err)
	got, err = f.store.ReadMediaRequestReceipt(ctx, f.owner, "second")
	mediaRequestWantReceipt(t, got, second, err)
	_, err = f.store.SubmitMediaRequest(ctx, f.owner, mediaRequestIntent(), "withdraw")
	mediaRequestWantError(t, err, ports.ErrMediaRequestConflict)
	if n := f.count(t, "SELECT count(*) FROM audit_log WHERE target_type='media_request'"); n != 4 {
		t.Fatal("recovery published again", n)
	}
}

func TestMediaRequestReadAccountAndCursorPG(t *testing.T) {
	f := newMediaRequestFixture(t)
	ctx := t.Context()
	own := f.submit(t, f.owner, "own")
	foreign := f.submit(t, f.other, "foreign")
	reader := postgres.NewMediaRequestStore(f.pool, nil, nil)
	for _, test := range []struct {
		actor *ports.Actor
		count int
	}{{f.owner, 1}, {f.admin, 2}} {
		rows, err := reader.ListMediaRequests(ctx, test.actor, 100, nil)
		if err != nil || len(rows) != test.count {
			t.Fatal("list visibility", rows, err)
		}
	}
	rows, err := reader.ListMediaRequests(ctx, f.owner, 100, &own.Request.ID)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatal("empty page must be []", rows, err)
	}
	_, err = reader.ListMediaRequests(ctx, f.owner, 1, &foreign.Request.ID)
	mediaRequestWantError(t, err, ports.ErrForbidden)
	_, err = reader.ReadMediaRequest(ctx, f.owner, foreign.Request.ID)
	mediaRequestWantError(t, err, ports.ErrForbidden)
	missing := uuid.New()
	_, err = reader.ListMediaRequests(ctx, f.owner, 1, &missing)
	mediaRequestWantError(t, err, ports.ErrNotFound)
	for _, limit := range []int{0, 101} {
		_, err = reader.ListMediaRequests(ctx, f.owner, limit, nil)
		mediaRequestWantError(t, err, ports.ErrRejected)
	}
	_, err = reader.ReadMediaRequestReceipt(ctx, f.owner, "missing")
	mediaRequestWantError(t, err, ports.ErrNotFound)
	_, err = reader.SubmitMediaRequest(ctx, f.owner, mediaRequestIntent(), "write-without-deps")
	mediaRequestWantError(t, err, ports.ErrUnavailable)
	token := *f.owner
	token.Scopes = []string{}
	_, err = reader.ReadMediaRequest(ctx, &token, own.Request.ID)
	mediaRequestWantError(t, err, ports.ErrForbidden)
	f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.owner.ID))
	_, err = reader.ReadMediaRequest(ctx, f.owner, own.Request.ID)
	mediaRequestWantError(t, err, ports.ErrForbidden)
	_, err = reader.ReadMediaRequestReceipt(ctx, f.owner, "own")
	mediaRequestWantError(t, err, ports.ErrForbidden)
	_, err = reader.ListMediaRequests(ctx, f.owner, 1, nil)
	mediaRequestWantError(t, err, ports.ErrForbidden)
	f.exec(t, "UPDATE app_user SET role='member' WHERE id=$1", uuid.MustParse(f.admin.ID))
	_, err = reader.ListMediaRequests(ctx, f.admin, 1, nil)
	mediaRequestWantError(t, err, ports.ErrForbidden)
	// A real SELECT error must not masquerade as a successful empty collection.
	f.exec(t, "ALTER TABLE media_request RENAME TO media_request_unavailable")
	_, err = reader.ListMediaRequests(ctx, f.other, 1, nil)
	mediaRequestWantError(t, err, ports.ErrUnavailable)
}

func TestMediaRequestSubmitTailRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := newMediaRequestFixture(t)
			f.exec(t, `CREATE FUNCTION media_request_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected tail failure' USING ERRCODE='23514'; END $$`)
			f.exec(t, "CREATE CONSTRAINT TRIGGER media_request_test_tail AFTER INSERT ON "+table+" DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION media_request_test_fail()")
			_, err := f.store.SubmitMediaRequest(t.Context(), f.owner, mediaRequestIntent(), "retry")
			mediaRequestWantError(t, err, ports.ErrUnavailable)
			for _, query := range []string{"SELECT count(*) FROM media_request", "SELECT count(*) FROM audit_log WHERE target_type='media_request'", "SELECT count(*) FROM outbox_event WHERE aggregate_type='media_request'", "SELECT count(*) FROM idempotency_record WHERE idempotency_key='media.request.command:retry'"} {
				if n := f.count(t, query); n != 0 {
					t.Fatal("partial transaction", query, n)
				}
			}
			f.exec(t, "DROP TRIGGER media_request_test_tail ON "+table)
			f.exec(t, "DROP FUNCTION media_request_test_fail()")
			r := f.submit(t, f.owner, "retry")
			got, err := f.store.SubmitMediaRequest(t.Context(), f.owner, mediaRequestIntent(), "retry")
			mediaRequestWantReceipt(t, got, r, err)
		})
	}
}

func TestMediaRequestValidationAndLimitPG(t *testing.T) {
	f := newMediaRequestFixture(t)
	ctx := t.Context()
	for _, key := range []string{"", " white", "a/b", "中文"} {
		_, err := f.store.SubmitMediaRequest(ctx, f.owner, mediaRequestIntent(), key)
		mediaRequestWantError(t, err, ports.ErrRejected)
	}
	for _, in := range []mediarequest.Intent{{MediaType: "tv", TMDBID: 9007199254740992}, {MediaType: "tv", TMDBID: 1, Seasons: []int{1, 1}}, {MediaType: "movie", TMDBID: 1, Seasons: []int{1}}} {
		_, err := f.store.SubmitMediaRequest(ctx, f.owner, in, "invalid")
		mediaRequestWantError(t, err, ports.ErrRejected)
	}
	first := f.submit(t, f.owner, "first")
	for i := 1; i < 100; i++ {
		f.submit(t, f.owner, fmt.Sprintf("pending-%d", i))
	}
	_, err := f.store.SubmitMediaRequest(ctx, f.owner, mediaRequestIntent(), "over-limit")
	mediaRequestWantError(t, err, ports.ErrMediaRequestLimit)
	got, err := f.store.SubmitMediaRequest(ctx, f.owner, mediaRequestIntent(), "first")
	mediaRequestWantReceipt(t, got, first, err)
	f.submit(t, f.other, "other-owner")
	_, err = f.store.CloseMediaRequest(ctx, f.owner, first.Request.ID, ports.MediaRequestCloseInput{ExpectedVersion: 1, Action: "withdraw"}, "free-slot")
	if err != nil {
		t.Fatal(err)
	}
	f.submit(t, f.owner, "after-withdraw")
}

func TestMediaRequestSchema107HistoricalEmptyDownRoundTripPG(t *testing.T) {
	f := newMediaRequestFixture(t)
	if version, err := postgres.Rollback(t.Context(), f.dsn, f.history); err != nil || version != 106 {
		t.Fatal("empty historical down", version, err)
	}
	if version, err := postgres.Migrate(t.Context(), f.dsn, f.history); err != nil || version != 107 {
		t.Fatal("historical up", version, err)
	}
	f.submit(t, f.owner, "after-roundtrip")
}

func TestMediaRequestSchema107NonemptyDownRefusalPG(t *testing.T) {
	f := newMediaRequestFixture(t)
	r := f.submit(t, f.owner, "history")
	if _, err := postgres.Rollback(t.Context(), f.dsn, f.history); err == nil {
		t.Fatal("nonempty media request history downgraded")
	}
	got, err := f.store.ReadMediaRequestReceipt(context.Background(), f.owner, "history")
	mediaRequestWantReceipt(t, got, r, err)
}
