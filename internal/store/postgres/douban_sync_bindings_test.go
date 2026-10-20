package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func doubanBindingEndpoints(t *testing.T, f *doubanSyncFixture) *ports.DoubanSyncBindings {
	t.Helper()
	ids := []uuid.UUID{uuid.MustParse("20000000-0000-0000-0000-000000000001"), uuid.MustParse("10000000-0000-0000-0000-000000000001")}
	for _, id := range ids {
		f.exec(t, `INSERT INTO indexer(id,name,url,api_key_encrypted,health) VALUES($1,$2,'https://fixture.invalid','fixture','online')`, id, id.String())
		f.exec(t, `INSERT INTO indexer_lifecycle(indexer_id,enabled) VALUES($1,true)`, id)
	}
	d := uuid.New()
	f.exec(t, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted) VALUES($1,'douban fixture','qbittorrent','https://fixture.invalid','fixture','fixture')`, d)
	return &ports.DoubanSyncBindings{IndexerIDs: ids, DownloaderID: &d}
}

func TestDoubanSyncBindingsCanonicalAndLegacy(t *testing.T) {
	ctx := context.Background()
	f := newDoubanSyncFixture(t)
	old := ports.DoubanSyncInput{DoubanUID: "exherb"}
	first := f.save(t, old, "plain")
	var hash string
	var input []byte
	wishNoError(t, f.p.QueryRow(ctx, `SELECT request_hash,input FROM douban_sync_config_receipt`).Scan(&hash, &input))
	if hash != "39496f37ad494bc6d5527ec81f93dbeb83d5a273b921d2467f7fc5a96ea26fc0" || first.Config.Bindings != nil {
		t.Fatal("old hash changed", hash)
	}
	var body map[string]json.RawMessage
	wishNoError(t, json.Unmarshal(input, &body))
	if _, ok := body["bindings"]; ok {
		t.Fatal("legacy input gained bindings")
	}
	b := doubanBindingEndpoints(t, f)
	in := f.input()
	in.ExpectedVersion = 1
	in.Bindings = b
	saved := f.save(t, in, "explicit")
	if !slices.IsSortedFunc(saved.Config.Bindings.IndexerIDs, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) }) {
		t.Fatal("not sorted")
	}
	if b.IndexerIDs[0] == saved.Config.Bindings.IndexerIDs[0] {
		t.Fatal("caller slice mutated")
	}
	// Canonical permutations share the same original-command hash.
	slices.Reverse(b.IndexerIDs)
	replay := f.save(t, in, "explicit")
	if !reflect.DeepEqual(saved, replay) {
		t.Fatal("permutation replay changed")
	}
	in.Bindings = nil
	in.ExpectedVersion = 2
	inherited := f.save(t, in, "inherit")
	if !reflect.DeepEqual(inherited.Config.Bindings, saved.Config.Bindings) {
		t.Fatal("inheritance lost")
	}
	wishNoError(t, f.p.QueryRow(ctx, `SELECT input FROM douban_sync_config_receipt WHERE version=3`).Scan(&input))
	wishNoError(t, json.Unmarshal(input, &body))
	if _, ok := body["bindings"]; ok {
		t.Fatal("inheritance filled original request")
	}
	in.ExpectedVersion = 3
	in.Bindings = &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{}}
	cleared := f.save(t, in, "clear")
	if cleared.Config.Bindings == nil || cleared.Config.Bindings.IndexerIDs == nil || len(cleared.Config.Bindings.IndexerIDs) != 0 || cleared.Config.Bindings.DownloaderID != nil {
		t.Fatal("clear lost empty object")
	}
	current, e := f.s.ReadDoubanSyncConfig(ctx, f.a)
	wishNoError(t, e)
	if !reflect.DeepEqual(current, &cleared.Config) {
		t.Fatal("current snapshot differs")
	}
	for name, bad := range map[string]*ports.DoubanSyncBindings{"nil ids": {}, "downloader only": {IndexerIDs: []uuid.UUID{}, DownloaderID: b.DownloaderID}, "zero id": {IndexerIDs: []uuid.UUID{uuid.Nil}, DownloaderID: b.DownloaderID}, "duplicate": {IndexerIDs: []uuid.UUID{b.IndexerIDs[0], b.IndexerIDs[0]}, DownloaderID: b.DownloaderID}, "no downloader": {IndexerIDs: []uuid.UUID{b.IndexerIDs[0]}}, "zero downloader": {IndexerIDs: []uuid.UUID{}, DownloaderID: new(uuid.UUID)}, "too many": {IndexerIDs: make([]uuid.UUID, 65), DownloaderID: b.DownloaderID}} {
		t.Run(name, func(t *testing.T) {
			in.ExpectedVersion = 4
			in.Bindings = bad
			_, e := f.s.SaveDoubanSyncConfig(ctx, f.a, in, "invalid")
			requireDoubanError(t, e, ports.ErrRejected)
		})
	}
	f.counts(t, 4)
}

func TestDoubanSyncBindingsHistoricalReplay(t *testing.T) {
	ctx := context.Background()
	f := newDoubanSyncFixture(t)
	b := doubanBindingEndpoints(t, f)
	in := f.input()
	old := f.save(t, in, "old")
	in.Bindings = b
	in.ExpectedVersion = 1
	bound := f.save(t, in, "bound")
	var beforeInput, beforeSnapshot []byte
	var beforeHash string
	wishNoError(t, f.p.QueryRow(ctx, `SELECT input,snapshot,request_hash FROM douban_sync_config_receipt WHERE version=2`).Scan(&beforeInput, &beforeSnapshot, &beforeHash))
	off := in
	off.ExpectedVersion = 2
	off.Enabled = false
	off.Bindings = nil
	f.save(t, off, "off")
	f.exec(t, `DELETE FROM indexer_lifecycle`)
	f.exec(t, `DELETE FROM indexer`)
	f.exec(t, `DELETE FROM download_client`)
	r := f.save(t, in, "bound")
	if !reflect.DeepEqual(r, bound) {
		t.Fatal("historical endpoint replay changed")
	}
	r = f.save(t, f.input(), "old")
	if !reflect.DeepEqual(r, old) {
		t.Fatal("plain replay gained bindings")
	}
	var afterInput, afterSnapshot []byte
	var afterHash string
	wishNoError(t, f.p.QueryRow(ctx, `SELECT input,snapshot,request_hash FROM douban_sync_config_receipt WHERE version=2`).Scan(&afterInput, &afterSnapshot, &afterHash))
	if string(beforeInput) != string(afterInput) || string(beforeSnapshot) != string(afterSnapshot) || beforeHash != afterHash {
		t.Fatal("immutable bytes changed")
	}
	f.counts(t, 3)
}

func TestDoubanSyncBindingsVerifiedChain(t *testing.T) {
	ctx := context.Background()
	for _, damage := range []string{"missing current", "missing predecessor", "corrupt hash", "corrupt snapshot", "invented inheritance"} {
		t.Run(damage, func(t *testing.T) {
			f := newDoubanSyncFixture(t)
			in := f.input()
			f.save(t, in, "initial")
			b := doubanBindingEndpoints(t, f)
			in.ExpectedVersion = 1
			in.Bindings = b
			f.save(t, in, "explicit")
			in.Bindings = nil
			for v := int64(2); v < 70; v++ {
				in.ExpectedVersion = v
				f.save(t, in, fmt.Sprintf("inherit-%d", v))
			}
			_, e := f.s.ReadDoubanSyncConfig(ctx, f.a)
			wishNoError(t, e)
			switch damage {
			case "missing current":
				f.mutateReceipt(t, `DELETE FROM douban_sync_config_receipt WHERE version=70`)
			case "missing predecessor":
				f.mutateReceipt(t, `DELETE FROM douban_sync_config_receipt WHERE version=35`)
			case "corrupt hash":
				f.mutateReceipt(t, `UPDATE douban_sync_config_receipt SET request_hash=repeat('0',64) WHERE version=35`)
			case "corrupt snapshot":
				f.mutateReceipt(t, `UPDATE douban_sync_config_receipt SET snapshot=jsonb_set(snapshot,'{owner_id}',to_jsonb(gen_random_uuid()::text)) WHERE version=35`)
			case "invented inheritance":
				f.mutateReceipt(t, `UPDATE douban_sync_config_receipt SET snapshot=snapshot-'bindings' WHERE version=35`)
			}
			_, e = f.s.ReadDoubanSyncConfig(ctx, f.a)
			requireDoubanError(t, e, ports.ErrSubscriptionBindingsConflict)
			in.ExpectedVersion = 70
			_, e = f.s.SaveDoubanSyncConfig(ctx, f.a, in, "new")
			requireDoubanError(t, e, ports.ErrSubscriptionBindingsConflict)
			store := postgres.NewDoubanWishSyncStore(f.p, idgen.New())
			wishError(t, store.EnqueueDue(ctx))
			var jobs int
			wishNoError(t, f.p.QueryRow(ctx, `SELECT count(*) FROM job`).Scan(&jobs))
			if jobs != 0 {
				t.Fatal("invalid receipt enqueued")
			}
		})
	}
}

func TestDoubanSyncBindingsExecutionSnapshot(t *testing.T) {
	ctx := context.Background()
	for _, damage := range []string{"disabled indexer", "deleted downloader", "missing receipt", "corrupt predecessor"} {
		t.Run(damage, func(t *testing.T) {
			f := newWishFixture(t, false)
			b := doubanBindingEndpoints(t, f.doubanSyncFixture)
			in := f.input()
			in.TVRootID = nil
			in.ExpectedVersion = 1
			in.Bindings = b
			f.save(t, in, "bound")
			in.ExpectedVersion = 2
			in.Bindings = nil
			f.save(t, in, "inherited")
			_, p := f.acquire(t)
			original := p.Snapshot()
			changed := p.Snapshot()
			changed.Config.Bindings.IndexerIDs[0] = uuid.New()
			*changed.Config.Bindings.DownloaderID = uuid.New()
			if !reflect.DeepEqual(original, p.Snapshot()) {
				t.Fatal("permit aliases bindings")
			}
			switch damage {
			case "disabled indexer":
				f.exec(t, `UPDATE indexer_lifecycle SET enabled=false`)
			case "deleted downloader":
				f.exec(t, `DELETE FROM download_client`)
			case "missing receipt":
				f.mutateReceipt(t, `DELETE FROM douban_sync_config_receipt WHERE version=2`)
			case "corrupt predecessor":
				f.mutateReceipt(t, `UPDATE douban_sync_config_receipt SET request_hash=repeat('0',64) WHERE version=2`)
			}
			wishError(t, p.Check(ctx))
			_, e := p.StorePage(ctx, wishPage(p, 1, 1))
			wishError(t, e)
			wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_item`, 0)
		})
	}
	t.Run("enqueue rejects disabled endpoint", func(t *testing.T) {
		f := newDoubanSyncFixture(t)
		b := doubanBindingEndpoints(t, f)
		in := f.input()
		in.Bindings = b
		f.save(t, in, "bound")
		f.exec(t, `UPDATE indexer_lifecycle SET enabled=false`)
		s := postgres.NewDoubanWishSyncStore(f.p, idgen.New())
		wishNoError(t, s.EnqueueDue(ctx))
		var n int
		wishNoError(t, f.p.QueryRow(ctx, `SELECT count(*) FROM job`).Scan(&n))
		if n != 0 {
			t.Fatal("disabled endpoint enqueued")
		}
	})
	t.Run("enqueue propagates canceled endpoint lock", func(t *testing.T) {
		f := newDoubanSyncFixture(t)
		b := doubanBindingEndpoints(t, f)
		in := f.input()
		in.Bindings = b
		f.save(t, in, "bound")
		tx, e := f.p.Begin(ctx)
		wishNoError(t, e)
		defer func() { _ = tx.Rollback(ctx) }()
		_, e = tx.Exec(ctx, `SELECT id FROM indexer WHERE id=$1 FOR UPDATE`, b.IndexerIDs[0])
		wishNoError(t, e)
		canceled, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		s := postgres.NewDoubanWishSyncStore(f.p, idgen.New())
		go func() { done <- s.EnqueueDue(canceled) }()
		waitDoubanSyncLock(t, f.p, "FROM indexer")
		cancel()
		select {
		case e := <-done:
			requireDoubanError(t, e, ports.ErrUnavailable)
		case <-time.After(5 * time.Second):
			t.Fatal("canceled enqueue blocked")
		}
		wishNoError(t, tx.Rollback(ctx))
		var n int
		wishNoError(t, f.p.QueryRow(ctx, `SELECT count(*) FROM job`).Scan(&n))
		if n != 0 {
			t.Fatal("canceled enqueue wrote jobs")
		}
	})
}

func TestDoubanSyncBindingsConcurrencyAndLockWait(t *testing.T) {
	ctx := context.Background()
	f := newDoubanSyncFixture(t)
	b := doubanBindingEndpoints(t, f)
	in := f.input()
	in.Bindings = b
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, e := f.s.SaveDoubanSyncConfig(ctx, f.a, in, "same"); errs <- e })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		wishNoError(t, e)
	}
	f.counts(t, 1)
	in.ExpectedVersion = 1
	errs = make(chan error, 2)
	for i := range 2 {
		wg.Go(func() { _, e := f.s.SaveDoubanSyncConfig(ctx, f.a, in, fmt.Sprintf("version-%d", i)); errs <- e })
	}
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for e := range errs {
		switch e {
		case nil:
			ok++
		case ports.ErrSubscriptionBindingsConflict:
			conflict++
		default:
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	for _, change := range []string{"endpoint deleted", "endpoint disabled", "actor revoked", "lease expired"} {
		t.Run(change, func(t *testing.T) {
			g := newWishFixture(t, false)
			binding := doubanBindingEndpoints(t, g.doubanSyncFixture)
			request := g.input()
			request.ExpectedVersion = 1
			request.Bindings = binding
			g.save(t, request, "bound")
			tx, e := g.p.Begin(ctx)
			wishNoError(t, e)
			defer func() { _ = tx.Rollback(ctx) }()
			_, e = tx.Exec(ctx, `SELECT id FROM indexer WHERE id=$1 FOR UPDATE`, binding.IndexerIDs[0])
			wishNoError(t, e)
			done := make(chan error, 1)
			switch change {
			case "actor revoked": // Current actor lock is tested while the caller is blocked.
				_, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, g.a.ID)
				wishNoError(t, e)
				request.ExpectedVersion = 2
				go func() { _, e := g.s.SaveDoubanSyncConfig(ctx, g.a, request, "wait"); done <- e }()
				waitDoubanSyncLock(t, g.p, "FROM app_user")
			case "lease expired":
				_ = tx.Rollback(ctx)
				lease := g.claim(t)
				g.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '1 second' WHERE id=$1`, lease.ID)
				tx, e = g.p.Begin(ctx)
				wishNoError(t, e)
				_, e = tx.Exec(ctx, `SELECT id FROM indexer WHERE id=$1 FOR UPDATE`, binding.IndexerIDs[0])
				wishNoError(t, e)
				go func() {
					p, e := g.store.Acquire(ctx, lease)
					if p != nil {
						p.Close()
					}
					done <- e
				}()
				waitDoubanSyncLock(t, g.p, "FROM indexer")
				_, e = tx.Exec(ctx, `SELECT pg_sleep(1.1)`)
				wishNoError(t, e)
			default:
				request.ExpectedVersion = 2
				go func() { _, e := g.s.SaveDoubanSyncConfig(ctx, g.a, request, "wait"); done <- e }()
				waitDoubanSyncLock(t, g.p, "FROM indexer")
				if change == "endpoint deleted" {
					_, e = tx.Exec(ctx, `DELETE FROM indexer_lifecycle WHERE indexer_id=$1`, binding.IndexerIDs[0])
					wishNoError(t, e)
					_, e = tx.Exec(ctx, `DELETE FROM indexer WHERE id=$1`, binding.IndexerIDs[0])
				} else {
					_, e = tx.Exec(ctx, `UPDATE indexer_lifecycle SET enabled=false WHERE indexer_id=$1`, binding.IndexerIDs[0])
				}
				wishNoError(t, e)
			}
			wishNoError(t, tx.Commit(ctx))
			select {
			case e := <-done:
				wishError(t, e)
			case <-time.After(5 * time.Second):
				t.Fatal("blocked operation did not finish")
			}
		})
	}
}

func TestDoubanSyncBindingsAuditEventRollback(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event"} {
		t.Run(table, func(t *testing.T) {
			f := newDoubanSyncFixture(t)
			b := doubanBindingEndpoints(t, f)
			in := f.input()
			first := f.save(t, in, "old")
			in.ExpectedVersion = 1
			in.Bindings = b
			f.exec(t, `CREATE FUNCTION fail_binding_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture';END $$;CREATE TRIGGER fail_binding_tail BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_binding_tail()`)
			_, e := f.s.SaveDoubanSyncConfig(context.Background(), f.a, in, "bound")
			requireDoubanError(t, e, ports.ErrUnavailable)
			current, e := f.s.ReadDoubanSyncConfig(context.Background(), f.a)
			wishNoError(t, e)
			if !reflect.DeepEqual(current, &first.Config) {
				t.Fatal("partial binding save")
			}
			f.counts(t, 1)
			f.exec(t, `DROP TRIGGER fail_binding_tail ON `+table)
			f.save(t, in, "bound")
			f.counts(t, 2)
		})
	}
}
