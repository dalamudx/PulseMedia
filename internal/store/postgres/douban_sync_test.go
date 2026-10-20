package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const doubanSyncMigrationsDir = "../../../migrations"

type doubanSyncFixture struct {
	p         *pgxpool.Pool
	s         *postgres.DoubanSyncConfigStore
	a         *ports.Actor
	movie, tv uuid.UUID
	dsn       string
}

func newDoubanSyncFixture(t *testing.T) *doubanSyncFixture {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	version, err := postgres.Migrate(ctx, dsn, doubanSyncMigrationsDir)
	if err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatal("migrate", version, err)
	}
	p, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	var pgVersion string
	if err = p.QueryRow(ctx, "SHOW server_version_num").Scan(&pgVersion); err != nil || pgVersion != "180006" {
		t.Fatal("requires PG18.6", pgVersion, err)
	}
	f := &doubanSyncFixture{p: p, s: postgres.NewDoubanSyncConfigStore(p, idgen.New()), movie: uuid.New(), tv: uuid.New(), dsn: dsn}
	f.a = f.user(t, "admin")
	for _, r := range []struct {
		id   uuid.UUID
		kind string
	}{{f.movie, "movie"}, {f.tv, "tv"}} {
		f.exec(t, `INSERT INTO library_root(id,path,name,media_type) VALUES($1,$2,$3,$3)`, r.id, "/douban/"+r.kind, r.kind)
	}
	return f
}
func (f *doubanSyncFixture) user(t *testing.T, role string) *ports.Actor {
	t.Helper()
	u, err := postgres.Queries(f.p).CreateUser(context.Background(), db.CreateUserParams{ID: uuid.New(), Username: uuid.NewString(), PasswordHash: "fixture", Role: role, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	return &ports.Actor{Type: ports.ActorTypeUser, ID: u.ID.String(), Role: role}
}
func (f *doubanSyncFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.p.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *doubanSyncFixture) input() ports.DoubanSyncInput {
	return ports.DoubanSyncInput{DoubanUID: "exherb", Enabled: true, MovieRootID: &f.movie, TVRootID: &f.tv}
}
func (f *doubanSyncFixture) save(t *testing.T, in ports.DoubanSyncInput, key string) *ports.DoubanSyncConfigReceipt {
	t.Helper()
	r, err := f.s.SaveDoubanSyncConfig(context.Background(), f.a, in, key)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *doubanSyncFixture) counts(t *testing.T, want int) {
	t.Helper()
	for _, table := range []string{"douban_sync_config_receipt", "audit_log", "outbox_event"} {
		var n int
		if err := f.p.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, n, want, err)
		}
	}
}
func requireDoubanError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error=%v want=%v", got, want)
	}
}

func TestDoubanSyncReadsAndAuthorization(t *testing.T) {
	f := newDoubanSyncFixture(t)
	ctx := context.Background()
	t.Run("absent is nil and receipt is not found", func(t *testing.T) {
		r, e := f.s.ReadDoubanSyncConfig(ctx, f.a)
		if r != nil || e != nil {
			t.Fatal(r, e)
		}
		_, e = f.s.ReadDoubanSyncConfigReceipt(ctx, f.a, "absent")
		requireDoubanError(t, e, ports.ErrNotFound)
		f.counts(t, 0)
	})
	original := f.save(t, f.input(), "create")
	t.Run("current GET has no writes", func(t *testing.T) {
		for range 2 {
			r, e := f.s.ReadDoubanSyncConfig(ctx, f.a)
			if e != nil || !reflect.DeepEqual(r, &original.Config) {
				t.Fatal(r, e)
			}
		}
		f.counts(t, 1)
	})
	member := f.user(t, "member")
	t.Run("member and admin each own only", func(t *testing.T) {
		r, e := f.s.ReadDoubanSyncConfig(ctx, member)
		if r != nil || e != nil {
			t.Fatal(r, e)
		}
		_, e = f.s.ReadDoubanSyncConfigReceipt(ctx, member, "create")
		requireDoubanError(t, e, ports.ErrNotFound)
		r2, e := f.s.SaveDoubanSyncConfig(ctx, member, ports.DoubanSyncInput{DoubanUID: "123"}, "create")
		if e != nil || r2.Config.OwnerID.String() != member.ID || r2.Config.ID == original.Config.ID {
			t.Fatal(r2, e)
		}
	})
	cases := []struct {
		name string
		a    *ports.Actor
		err  error
	}{{"anonymous", nil, ports.ErrUnauthorized}}
	for _, name := range []string{"token empty", "token scope", "service", "system", "bad UUID", "nil UUID", "bad role", "stale role"} {
		a := *f.a
		switch name {
		case "token empty":
			a.Scopes = []string{}
		case "token scope":
			a.Scopes = []string{"admin"}
		case "service":
			a.Type = ports.ActorTypeService
		case "system":
			a.Type = ports.ActorTypeSystem
		case "bad UUID":
			a.ID = "bad"
		case "nil UUID":
			a.ID = uuid.Nil.String()
		case "bad role":
			a.Role = "guest"
		case "stale role":
			a.Role = "member"
		}
		cases = append(cases, struct {
			name string
			a    *ports.Actor
			err  error
		}{name, &a, ports.ErrForbidden})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, e := f.s.ReadDoubanSyncConfig(ctx, tc.a)
			requireDoubanError(t, e, tc.err)
			_, e = f.s.ReadDoubanSyncConfigReceipt(ctx, tc.a, "create")
			requireDoubanError(t, e, tc.err)
			_, e = f.s.SaveDoubanSyncConfig(ctx, tc.a, f.input(), "forbidden")
			requireDoubanError(t, e, tc.err)
		})
	}
	t.Run("disabled current account", func(t *testing.T) {
		f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", f.a.ID)
		_, e := f.s.ReadDoubanSyncConfigReceipt(ctx, f.a, "create")
		requireDoubanError(t, e, ports.ErrForbidden)
		_, e = f.s.ReadDoubanSyncConfig(ctx, f.a)
		requireDoubanError(t, e, ports.ErrForbidden)
	})
}

func TestDoubanSyncConcurrentOriginalKey(t *testing.T) {
	f := newDoubanSyncFixture(t)
	ctx := context.Background()
	in := f.input()
	t.Run("concurrent replay writes once", func(t *testing.T) {
		var wg sync.WaitGroup
		out := make(chan *ports.DoubanSyncConfigReceipt, 8)
		errs := make(chan error, 8)
		for range 8 {
			wg.Go(func() { r, e := f.s.SaveDoubanSyncConfig(ctx, f.a, in, "original"); out <- r; errs <- e })
		}
		wg.Wait()
		close(out)
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		var first *ports.DoubanSyncConfigReceipt
		for r := range out {
			if first == nil {
				first = r
			}
			if !reflect.DeepEqual(first, r) {
				t.Fatal("unstable receipt")
			}
		}
		f.counts(t, 1)
	})
	t.Run("different keys same expected version have one winner", func(t *testing.T) {
		in.ExpectedVersion = 1
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i := range 2 {
			wg.Go(func() { _, e := f.s.SaveDoubanSyncConfig(ctx, f.a, in, fmt.Sprintf("next-%d", i)); errs <- e })
		}
		wg.Wait()
		close(errs)
		ok, conflict := 0, 0
		for e := range errs {
			if e == nil {
				ok++
			} else if errors.Is(e, ports.ErrSubscriptionBindingsConflict) {
				conflict++
			} else {
				t.Fatal(e)
			}
		}
		if ok != 1 || conflict != 1 {
			t.Fatal(ok, conflict)
		}
		f.counts(t, 2)
	})
	t.Run("old expected version replays original", func(t *testing.T) {
		in.ExpectedVersion = 0
		r, e := f.s.SaveDoubanSyncConfig(ctx, f.a, in, "original")
		if e != nil || r.Config.Version != 1 {
			t.Fatal(r, e)
		}
		f.counts(t, 2)
	})
	t.Run("same key changed body conflicts", func(t *testing.T) {
		in.DoubanUID = "different"
		_, e := f.s.SaveDoubanSyncConfig(ctx, f.a, in, "original")
		requireDoubanError(t, e, ports.ErrSubscriptionBindingsConflict)
		in.DoubanUID = "exherb"
		in.ExpectedVersion = 2
		_, e = f.s.SaveDoubanSyncConfig(ctx, f.a, in, "original")
		requireDoubanError(t, e, ports.ErrSubscriptionBindingsConflict)
		f.counts(t, 2)
	})
}

func TestDoubanSyncValidation(t *testing.T) {
	f := newDoubanSyncFixture(t)
	ctx := context.Background()
	nilID := uuid.Nil
	missing := uuid.New()
	cases := []struct {
		name string
		in   ports.DoubanSyncInput
		key  string
	}{
		{"missing enabled root", ports.DoubanSyncInput{DoubanUID: "x", Enabled: true}, "x"},
		{"nil root UUID", ports.DoubanSyncInput{DoubanUID: "x", MovieRootID: &nilID}, "x"},
		{"wrong movie type", ports.DoubanSyncInput{DoubanUID: "x", MovieRootID: &f.tv}, "x"},
		{"wrong tv type", ports.DoubanSyncInput{DoubanUID: "x", TVRootID: &f.movie}, "x"},
		{"negative version", ports.DoubanSyncInput{DoubanUID: "x", ExpectedVersion: -1}, "x"},
	}
	for _, uid := range []string{"", "_a", ".a", "-a", "https://douban.com/x", "a/b", "a b", "中文", "é", strings.Repeat("x", 65)} {
		cases = append(cases, struct {
			name string
			in   ports.DoubanSyncInput
			key  string
		}{"uid " + uid, ports.DoubanSyncInput{DoubanUID: uid}, "x"})
	}
	for _, key := range []string{"", "bad key", "a/b", "é", strings.Repeat("k", 161)} {
		cases = append(cases, struct {
			name string
			in   ports.DoubanSyncInput
			key  string
		}{"key " + key, ports.DoubanSyncInput{DoubanUID: "ok"}, key})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, e := f.s.SaveDoubanSyncConfig(ctx, f.a, tc.in, tc.key)
			requireDoubanError(t, e, ports.ErrRejected)
		})
	}
	for _, enabled := range []bool{false, true} {
		for _, kind := range []string{"movie", "tv"} {
			t.Run(fmt.Sprintf("missing provided %s enabled=%t", kind, enabled), func(t *testing.T) {
				in := ports.DoubanSyncInput{DoubanUID: "x", Enabled: enabled}
				if kind == "movie" {
					in.MovieRootID = &missing
				} else {
					in.TVRootID = &missing
				}
				_, e := f.s.SaveDoubanSyncConfig(ctx, f.a, in, "missing")
				requireDoubanError(t, e, ports.ErrNotFound)
			})
		}
	}
	f.counts(t, 0)
	t.Run("offline rejected enabled retained disabled", func(t *testing.T) {
		f.exec(t, "UPDATE library_root SET health='offline' WHERE id=$1", f.movie)
		in := f.input()
		_, e := f.s.SaveDoubanSyncConfig(ctx, f.a, in, "offline")
		requireDoubanError(t, e, ports.ErrRejected)
		in.Enabled = false
		in.DoubanUID = "A" + strings.Repeat("a", 60) + "_.-"
		f.save(t, in, strings.Repeat("K", 156)+":-_.")
		f.counts(t, 1)
	})
	t.Run("existing version zero conflicts", func(t *testing.T) {
		_, e := f.s.SaveDoubanSyncConfig(ctx, f.a, ports.DoubanSyncInput{DoubanUID: "x"}, "zero")
		requireDoubanError(t, e, ports.ErrSubscriptionBindingsConflict)
	})
}

// Only the superuser fixture can bypass the immutable receipt trigger.
func (f *doubanSyncFixture) mutateReceipt(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, "ALTER TABLE douban_sync_config_receipt DISABLE TRIGGER douban_sync_receipt_history_guard"); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, sql, args...); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, "ALTER TABLE douban_sync_config_receipt ENABLE TRIGGER douban_sync_receipt_history_guard"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
}

func TestDoubanSyncHistoricalReceipt(t *testing.T) {
	f := newDoubanSyncFixture(t)
	ctx := context.Background()
	original := f.save(t, f.input(), "original")
	t.Run("timestamp offsets preserve original receipt bytes", func(t *testing.T) {
		g := newDoubanSyncFixture(t)
		in := g.input()
		original := g.save(t, in, "offset")
		created := original.Config.CreatedAt.In(time.FixedZone("fixture east", 9*60*60)).Format(time.RFC3339Nano)
		updated := original.Config.UpdatedAt.In(time.FixedZone("fixture west", -5*60*60)).Format(time.RFC3339Nano)
		g.mutateReceipt(t, `UPDATE douban_sync_config_receipt SET snapshot=jsonb_set(jsonb_set(snapshot,'{created_at}',to_jsonb($1::text)),'{updated_at}',to_jsonb($2::text)) WHERE command_key='douban.sync.config:offset'`, created, updated)
		var before, after []byte
		if e := g.p.QueryRow(ctx, "SELECT snapshot FROM douban_sync_config_receipt").Scan(&before); e != nil {
			t.Fatal(e)
		}
		r, e := g.s.ReadDoubanSyncConfigReceipt(ctx, g.a, "offset")
		if e != nil {
			t.Fatal(e)
		}
		if !r.Config.CreatedAt.Equal(original.Config.CreatedAt) || !r.Config.UpdatedAt.Equal(original.Config.UpdatedAt) || r.Config.CreatedAt.Format(time.RFC3339Nano) != created || r.Config.UpdatedAt.Format(time.RFC3339Nano) != updated {
			t.Fatal("offset receipt was normalized", r)
		}
		replay, e := g.s.SaveDoubanSyncConfig(ctx, g.a, in, "offset")
		if e != nil || !reflect.DeepEqual(r, replay) {
			t.Fatal(replay, e)
		}
		if e = g.p.QueryRow(ctx, "SELECT snapshot FROM douban_sync_config_receipt").Scan(&after); e != nil {
			t.Fatal(e)
		}
		if string(before) != string(after) {
			t.Fatal("receipt bytes changed")
		}
		g.counts(t, 1)
		for _, field := range []string{"created_at", "updated_at"} {
			t.Run("different instant "+field, func(t *testing.T) {
				g.mutateReceipt(t, "UPDATE douban_sync_config_receipt SET snapshot=jsonb_set(snapshot,ARRAY[$1::text],to_jsonb($2::text))", field, original.Config.UpdatedAt.Add(time.Second).Format(time.RFC3339Nano))
				_, e := g.s.ReadDoubanSyncConfigReceipt(ctx, g.a, "offset")
				requireDoubanError(t, e, ports.ErrSubscriptionBindingsConflict)
				g.mutateReceipt(t, "UPDATE douban_sync_config_receipt SET snapshot=$1", before)
			})
		}
	})
	t.Run("disabled new version with changed root health", func(t *testing.T) {
		f.exec(t, "UPDATE library_root SET health='offline',media_type='tv' WHERE id=$1", f.movie)
		f.save(t, ports.DoubanSyncInput{DoubanUID: "another", ExpectedVersion: 1}, "off")
		r, e := f.s.ReadDoubanSyncConfigReceipt(ctx, f.a, "original")
		if e != nil || !reflect.DeepEqual(r, original) {
			t.Fatal(r, e)
		}
		r, e = f.s.SaveDoubanSyncConfig(ctx, f.a, f.input(), "original")
		if e != nil || !reflect.DeepEqual(r, original) {
			t.Fatal(r, e)
		}
		current, e := f.s.ReadDoubanSyncConfig(ctx, f.a)
		if e != nil || current.Enabled || current.Version != 2 {
			t.Fatal(current, e)
		}
		f.counts(t, 2)
	})
	t.Run("history cannot be erased or mutated", func(t *testing.T) {
		for _, sql := range []string{"DELETE FROM douban_sync_config_receipt", "TRUNCATE douban_sync_config_receipt", "UPDATE douban_sync_config_receipt SET request_hash=repeat('0',64)", "DELETE FROM douban_sync_config", "TRUNCATE douban_sync_config CASCADE"} {
			if _, e := f.p.Exec(ctx, sql); e == nil {
				t.Fatal("history mutation allowed", sql)
			}
		}
		f.counts(t, 2)
	})
	t.Run("independent historical binding validation", func(t *testing.T) {
		for _, column := range []string{"request_hash", "input", "snapshot"} {
			t.Run(column, func(t *testing.T) {
				var originalValue []byte
				var text string
				if column == "request_hash" {
					if e := f.p.QueryRow(ctx, "SELECT request_hash FROM douban_sync_config_receipt WHERE command_key='douban.sync.config:original'").Scan(&text); e != nil {
						t.Fatal(e)
					}
					f.mutateReceipt(t, "UPDATE douban_sync_config_receipt SET request_hash=repeat('0',64) WHERE command_key='douban.sync.config:original'")
				} else {
					if e := f.p.QueryRow(ctx, "SELECT "+column+" FROM douban_sync_config_receipt WHERE command_key='douban.sync.config:original'").Scan(&originalValue); e != nil {
						t.Fatal(e)
					}
					f.mutateReceipt(t, "UPDATE douban_sync_config_receipt SET "+column+"='{}' WHERE command_key='douban.sync.config:original'")
				}
				_, e := f.s.ReadDoubanSyncConfigReceipt(ctx, f.a, "original")
				requireDoubanError(t, e, ports.ErrSubscriptionBindingsConflict)
				if column == "request_hash" {
					f.mutateReceipt(t, "UPDATE douban_sync_config_receipt SET request_hash=$1 WHERE command_key='douban.sync.config:original'", text)
				} else {
					f.mutateReceipt(t, "UPDATE douban_sync_config_receipt SET "+column+"=$1 WHERE command_key='douban.sync.config:original'", originalValue)
				}
			})
		}
	})
}

func TestDoubanSyncTailRollback(t *testing.T) {
	for _, table := range []string{"douban_sync_config_receipt", "audit_log", "outbox_event"} {
		t.Run(table, func(t *testing.T) {
			f := newDoubanSyncFixture(t)
			ctx := context.Background()
			f.exec(t, "CREATE FUNCTION fail_douban_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'private fixture detail';END $$;CREATE TRIGGER fail_douban_tail BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION fail_douban_tail()")
			_, e := f.s.SaveDoubanSyncConfig(ctx, f.a, f.input(), "retry")
			requireDoubanError(t, e, ports.ErrUnavailable)
			if e.Error() != ports.ErrUnavailable.Error() {
				t.Fatal("SQL detail leaked")
			}
			f.counts(t, 0)
			r, e := f.s.ReadDoubanSyncConfig(ctx, f.a)
			if e != nil || r != nil {
				t.Fatal("partial config", r, e)
			}
			f.exec(t, "DROP TRIGGER fail_douban_tail ON "+table)
			f.save(t, f.input(), "retry")
			f.counts(t, 1)
			// Updating also rolls back the version, roots and current enabled state.
			f.exec(t, "CREATE TRIGGER fail_douban_tail BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION fail_douban_tail()")
			_, e = f.s.SaveDoubanSyncConfig(ctx, f.a, ports.DoubanSyncInput{DoubanUID: "changed", ExpectedVersion: 1}, "retry-update")
			requireDoubanError(t, e, ports.ErrUnavailable)
			r, e = f.s.ReadDoubanSyncConfig(ctx, f.a)
			if e != nil || r.Version != 1 || !r.Enabled {
				t.Fatal(r, e)
			}
			f.counts(t, 1)
			f.exec(t, "DROP TRIGGER fail_douban_tail ON "+table)
			f.save(t, ports.DoubanSyncInput{DoubanUID: "changed", ExpectedVersion: 1}, "retry-update")
			f.counts(t, 2)
			for _, name := range []string{"media", "subscription", "job", "library_file"} {
				var n int
				if e = f.p.QueryRow(ctx, "SELECT count(*) FROM "+name).Scan(&n); e != nil || n != 0 {
					t.Fatal("unexpected business side effect", name, n, e)
				}
			}
		})
	}
}

func waitDoubanSyncLock(t *testing.T, p *pgxpool.Pool, query string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		e := p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)`, "%"+query+"%").Scan(&waiting)
		if e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("lock wait not observed")
		case <-ticker.C:
		}
	}
}
func TestDoubanSyncLockWaitAuthorization(t *testing.T) {
	for _, operation := range []string{"save", "read", "receipt"} {
		for _, change := range []string{"demote", "disable"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				f := newDoubanSyncFixture(t)
				ctx := context.Background()
				f.save(t, f.input(), "original")
				tx, e := f.p.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = tx.Rollback(ctx) }()
				sql := "UPDATE app_user SET role='member' WHERE id=$1"
				if change == "disable" {
					sql = "UPDATE app_user SET is_active=false WHERE id=$1"
				}
				if _, e = tx.Exec(ctx, sql, f.a.ID); e != nil {
					t.Fatal(e)
				}
				done := make(chan error, 1)
				go func() {
					var err error
					switch operation {
					case "save":
						_, err = f.s.SaveDoubanSyncConfig(ctx, f.a, ports.DoubanSyncInput{DoubanUID: "new", ExpectedVersion: 1}, "new")
					case "read":
						_, err = f.s.ReadDoubanSyncConfig(ctx, f.a)
					case "receipt":
						_, err = f.s.ReadDoubanSyncConfigReceipt(ctx, f.a, "original")
					}
					done <- err
				}()
				waitDoubanSyncLock(t, f.p, "LockDoubanSyncOwner")
				if e = tx.Commit(ctx); e != nil {
					t.Fatal(e)
				}
				requireDoubanError(t, <-done, ports.ErrForbidden)
				f.counts(t, 1)
			})
		}
	}
	t.Run("copies input and actor before root wait", func(t *testing.T) {
		f := newDoubanSyncFixture(t)
		ctx := context.Background()
		tx, e := f.p.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, e = tx.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", f.movie); e != nil {
			t.Fatal(e)
		}
		root := f.movie
		in := ports.DoubanSyncInput{DoubanUID: "copy", Enabled: true, MovieRootID: &root}
		a := *f.a
		done := make(chan *ports.DoubanSyncConfigReceipt, 1)
		errs := make(chan error, 1)
		go func() { r, e := f.s.SaveDoubanSyncConfig(ctx, &a, in, "copy"); done <- r; errs <- e }()
		waitDoubanSyncLock(t, f.p, "LockDoubanSyncRoots")
		root = f.tv
		a.ID = uuid.NewString()
		a.Role = "member"
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		r := <-done
		if e = <-errs; e != nil || r.Config.OwnerID.String() != f.a.ID || r.Config.MovieRootID == nil || *r.Config.MovieRootID != f.movie {
			t.Fatal(r, e)
		}
	})
}

func TestDoubanSyncRestoreAndReadFailure(t *testing.T) {
	t.Run("isolation blocks save and allows DB reads", func(t *testing.T) {
		f := newDoubanSyncFixture(t)
		ctx := context.Background()
		f.save(t, f.input(), "original")
		f.exec(t, "UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid() WHERE singleton")
		_, e := f.s.SaveDoubanSyncConfig(ctx, f.a, ports.DoubanSyncInput{DoubanUID: "new", ExpectedVersion: 1}, "new")
		requireDoubanError(t, e, ports.ErrUnavailable)
		r, e := f.s.ReadDoubanSyncConfigReceipt(ctx, f.a, "original")
		if e != nil || r.Config.Version != 1 {
			t.Fatal(r, e)
		}
		f.counts(t, 1)
	})
	t.Run("guard held through tail and commit", func(t *testing.T) {
		f := newDoubanSyncFixture(t)
		ctx := context.Background()
		tx, e := f.p.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, e = tx.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", f.movie); e != nil {
			t.Fatal(e)
		}
		saveDone := make(chan error, 1)
		go func() { _, e := f.s.SaveDoubanSyncConfig(ctx, f.a, f.input(), "held"); saveDone <- e }()
		waitDoubanSyncLock(t, f.p, "LockDoubanSyncRoots")
		isolationDone := make(chan error, 1)
		go func() {
			_, e := f.p.Exec(ctx, "UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid() WHERE singleton")
			isolationDone <- e
		}()
		waitDoubanSyncLock(t, f.p, "UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid()")
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		if e = <-saveDone; e != nil {
			t.Fatal(e)
		}
		if e = <-isolationDone; e != nil {
			t.Fatal(e)
		}
		f.counts(t, 1)
	})
	t.Run("real query failure never masquerades as absence", func(t *testing.T) {
		f := newDoubanSyncFixture(t)
		ctx := context.Background()
		f.save(t, f.input(), "original")
		f.exec(t, "ALTER TABLE douban_sync_config RENAME TO hidden_douban_config")
		r, e := f.s.ReadDoubanSyncConfig(ctx, f.a)
		if r != nil {
			t.Fatal(r)
		}
		requireDoubanError(t, e, ports.ErrUnavailable)
		_, e = f.s.ReadDoubanSyncConfigReceipt(ctx, f.a, "original")
		requireDoubanError(t, e, ports.ErrUnavailable)
		f.exec(t, "ALTER TABLE hidden_douban_config RENAME TO douban_sync_config")
		f.exec(t, "ALTER TABLE douban_sync_config_receipt RENAME TO hidden_douban_receipt")
		_, e = f.s.ReadDoubanSyncConfigReceipt(ctx, f.a, "original")
		requireDoubanError(t, e, ports.ErrUnavailable)
	})
}

func TestDoubanSyncMigration95(t *testing.T) {
	ctx := context.Background()
	t.Run("94 to 95 to 94 empty", func(t *testing.T) {
		dsn := pgtest.GetCluster(t).NewTestDB(t)
		dir := t.TempDir()
		entries, e := os.ReadDir(doubanSyncMigrationsDir)
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name()[:5] > "00094" {
				continue
			}
			raw, e := os.ReadFile(filepath.Join(doubanSyncMigrationsDir, entry.Name()))
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(dir, entry.Name()), raw, 0600); e != nil {
				t.Fatal(e)
			}
		}
		v, e := postgres.Migrate(ctx, dsn, dir)
		if e != nil || v != 94 {
			t.Fatal(v, e)
		}
		v, e = postgres.Migrate(ctx, dsn, doubanSyncMigrationsDir)
		if e != nil || v != postgres.CurrentMigrationVersion {
			t.Fatal(v, e)
		}
		v, e = postgres.Rollback(ctx, dsn, doubanSyncMigrationsDir)
		if e != nil || v != 94 {
			t.Fatal(v, e)
		}
	})
	t.Run("nonempty downgrade refuses history loss", func(t *testing.T) {
		f := newDoubanSyncFixture(t)
		f.save(t, f.input(), "original")
		if _, e := postgres.Rollback(ctx, f.dsn, doubanSyncMigrationsDir); e == nil {
			t.Fatal("history lost")
		}
		r, e := f.s.ReadDoubanSyncConfigReceipt(ctx, f.a, "original")
		if e != nil || r.Config.Version != 1 {
			t.Fatal(r, e)
		}
		if e = postgres.CheckSchemaVersion(ctx, f.p, postgres.CurrentMigrationVersion); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("configuration without receipt also blocks downgrade", func(t *testing.T) {
		f := newDoubanSyncFixture(t)
		f.exec(t, "INSERT INTO douban_sync_config(id,owner_id,douban_uid,enabled,version) VALUES($1,$2,'x',false,1)", uuid.New(), f.a.ID)
		if _, e := postgres.Rollback(ctx, f.dsn, doubanSyncMigrationsDir); e == nil {
			t.Fatal("config lost")
		}
	})
}
