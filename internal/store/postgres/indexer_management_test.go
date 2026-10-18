package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type managementIDs struct{}

func (managementIDs) NewUUIDString() string { return uuid.NewString() }

func (managementIDs) NewUUID() uuid.UUID { return uuid.New() }

const managementHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const managementOtherHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func managementFixture(t *testing.T) (*pgxpool.Pool, string, *configurationSecrets, *ports.Actor, *postgres.IndexerManagementStore) {
	t.Helper()
	p, dsn, secrets := configurationFixture(t)
	m5IndexerSchemaVersion(t, dsn, 85)
	var version string
	if err := p.QueryRow(t.Context(), `SHOW server_version`).Scan(&version); err != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatalf("requires PostgreSQL 18.6: %s %v", version, err)
	}
	a := pgtest.CreateUserActor(t, db.New(p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	return p, dsn, secrets, a, postgres.NewIndexerManagementStore(p, managementIDs{}, secrets)
}
func managementCreate(t *testing.T, s *configurationSecrets) ports.IndexerMutation {
	t.Helper()
	urlCipher, e := s.EncryptSecret([]byte("https://private-user:private-password@site.test/private-path?passkey=private-passkey"))
	if e != nil {
		t.Fatal(e)
	}
	keyCipher, e := s.EncryptSecret([]byte("private-credential"))
	if e != nil {
		t.Fatal(e)
	}
	return ports.IndexerMutation{Action: ports.IndexerCreate, Name: "Site", Configuration: ports.PreparedIndexerConfiguration{Protocol: ports.IndexerProtocolTorznab, DisplayURL: "https://site.test", URLCiphertext: urlCipher, CredentialCiphertext: keyCipher, Categories: []int{2000}}}
}
func managementUpdate(r *ports.IndexerManagementReceipt) ports.IndexerMutation {
	return ports.IndexerMutation{Action: ports.IndexerUpdate, IndexerID: r.Indexer.ID, ExpectedVersion: r.Indexer.ConfigVersion, Name: "Edited", PreserveURL: true, PreserveCredential: true, Configuration: ports.PreparedIndexerConfiguration{Protocol: ports.IndexerProtocolRSS, Categories: []int{}}}
}
func managementMust(t *testing.T, s *postgres.IndexerManagementStore, a *ports.Actor, m ports.IndexerMutation, key string) *ports.IndexerManagementReceipt {
	t.Helper()
	r, e := s.MutateIndexer(t.Context(), a, m, key, managementHash)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func managementCount(t *testing.T, p *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if e := p.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func managementNoSecrets(t *testing.T, p *pgxpool.Pool, receipts ...*ports.IndexerManagementReceipt) {
	t.Helper()
	for _, table := range []string{"indexer", "indexer_configuration", "audit_log", "outbox_event", "idempotency_record"} {
		var raw []byte
		if e := p.QueryRow(t.Context(), "SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]') FROM "+table+" x").Scan(&raw); e != nil {
			t.Fatal(e)
		}
		for _, secret := range []string{"private-user", "private-password", "private-path", "private-passkey", "private-credential"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("secret leaked in %s", table)
			}
		}
	}
	for _, r := range receipts {
		raw, _ := json.Marshal(r)
		if bytes.Contains(raw, []byte("private-")) {
			t.Fatal("secret leaked in receipt")
		}
	}
}

func TestIndexerManagementCommandsPG(t *testing.T) {
	p, dsn, secrets, a, store := managementFixture(t)
	ctx := t.Context()
	q := db.New(p)
	create := managementCreate(t, secrets)
	// Concurrent first calls must allocate one endpoint and return the same stable receipt.
	const contenders = 8
	var wg sync.WaitGroup
	receipts := make(chan *ports.IndexerManagementReceipt, contenders)
	failures := make(chan error, contenders)
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := store.MutateIndexer(ctx, a, create, "create-race", managementHash)
			receipts <- r
			failures <- e
		}()
	}
	wg.Wait()
	close(receipts)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	var created *ports.IndexerManagementReceipt
	for r := range receipts {
		if created == nil {
			created = r
		}
		if !reflect.DeepEqual(created, r) {
			t.Fatal("unstable concurrent create receipt")
		}
	}
	if managementCount(t, p, "indexer") != 1 || managementCount(t, p, "outbox_event") != 1 || managementCount(t, p, "audit_log") != 1 || managementCount(t, p, "idempotency_record") != 1 {
		t.Fatal("duplicate command effects")
	}
	original, e := q.GetIndexerByID(ctx, created.Indexer.ID)
	if e != nil {
		t.Fatal(e)
	}
	oldConfig, e := q.GetIndexerConfiguration(ctx, original.ID)
	if e != nil {
		t.Fatal(e)
	}
	if original.Url != "https://site.test" || oldConfig.BehaviorVersion != "explicit-v1" || oldConfig.ConfigurationVersion != 1 || !bytes.Equal(original.ApiKeyEncrypted, create.Configuration.CredentialCiphertext) || !bytes.Equal(oldConfig.UrlEncrypted, create.Configuration.URLCiphertext) {
		t.Fatal("incorrect encrypted create")
	}
	resolved, modes, e := postgres.ResolveIndexerConfiguration(ctx, q, secrets, original)
	if e != nil || modes.LegacyV1 || modes.Protocol != ports.IndexerProtocolTorznab || !strings.Contains(resolved.Url, "private-path") {
		t.Fatal("configuration failed to resolve", e)
	}
	// Retained FK evidence survives deletion.
	queryID := uuid.New()
	configurationExec(t, p, `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state,results_count) VALUES($1,$2,'search','historic',1,1,'succeeded',0)`, queryID, original.ID)
	// Same raw key binds both payload and action.
	if _, e = store.MutateIndexer(ctx, a, create, "create-race", managementOtherHash); !errors.Is(e, ports.ErrIndexerCommandConflict) {
		t.Fatal("changed payload accepted", e)
	}
	changedAction := managementUpdate(created)
	if _, e = store.MutateIndexer(ctx, a, changedAction, "create-race", managementHash); !errors.Is(e, ports.ErrIndexerCommandConflict) {
		t.Fatal("changed action accepted", e)
	}
	update := managementUpdate(created)
	results := make(chan *ports.IndexerManagementReceipt, 2)
	errs := make(chan error, 2)
	for _, key := range []string{"update-first", "update-second"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			r, e := store.MutateIndexer(ctx, a, update, key, managementHash)
			results <- r
			errs <- e
		}(key)
	}
	wg.Wait()
	close(results)
	close(errs)
	successes, conflicts := 0, 0
	for e := range errs {
		if e == nil {
			successes++
		} else if errors.Is(e, ports.ErrIndexerCommandConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("version race not fenced", successes, conflicts)
	}
	var updated *ports.IndexerManagementReceipt
	for r := range results {
		if r != nil {
			updated = r
		}
	}
	row, e := q.GetIndexerByID(ctx, original.ID)
	if e != nil {
		t.Fatal(e)
	}
	config, e := q.GetIndexerConfiguration(ctx, row.ID)
	if e != nil {
		t.Fatal(e)
	}
	if row.ConfigVersion != 2 || config.ConfigurationVersion != 2 || config.Protocol != "rss" || !bytes.Equal(row.ApiKeyEncrypted, original.ApiKeyEncrypted) || !bytes.Equal(config.UrlEncrypted, oldConfig.UrlEncrypted) || string(row.Categories) != "[]" {
		t.Fatal("preservation or version mismatch")
	}
	clear := managementUpdate(updated)
	clear.PreserveCredential = false
	clear.Configuration.CredentialCiphertext, e = secrets.EncryptSecret([]byte(""))
	if e != nil {
		t.Fatal(e)
	}
	cleared := managementMust(t, store, a, clear, "credential-clear")
	row, e = q.GetIndexerByID(ctx, original.ID)
	if e != nil {
		t.Fatal(e)
	}
	plain, e := secrets.DecryptSecret(row.ApiKeyEncrypted)
	if e != nil || len(plain) != 0 || len(row.ApiKeyEncrypted) == 0 || bytes.Equal(row.ApiKeyEncrypted, original.ApiKeyEncrypted) {
		t.Fatal("explicit clear failed")
	}
	del := ports.IndexerMutation{Action: ports.IndexerDelete, IndexerID: row.ID, ExpectedVersion: 3}
	deleted := managementMust(t, store, a, del, "delete-command")
	if !deleted.Deleted || deleted.Indexer.ConfigVersion != 4 || deleted.Indexer.Health != "offline" {
		t.Fatal("bad deletion receipt")
	}
	available, e := postgres.IndexerAvailableForExecution(ctx, q, row.ID)
	if e != nil || available {
		t.Fatal("tombstone executable", e)
	}
	list, e := store.ListManagedIndexers(ctx, a)
	if e != nil || len(list) != 0 {
		t.Fatal("tombstone listed", e)
	}
	if managementCount(t, p, "indexer_query") != 1 || managementCount(t, p, "indexer") != 1 || managementCount(t, p, "indexer_configuration") != 1 {
		t.Fatal("history deleted")
	}
	for _, c := range []struct {
		m ports.IndexerMutation
		k string
		r *ports.IndexerManagementReceipt
	}{{create, "create-race", created}, {clear, "credential-clear", cleared}, {del, "delete-command", deleted}} {
		got, e := store.MutateIndexer(ctx, a, c.m, c.k, managementHash)
		if e != nil || !reflect.DeepEqual(got, c.r) {
			t.Fatal("historical replay changed", e)
		}
	}
	if managementCount(t, p, "outbox_event") != 4 || managementCount(t, p, "audit_log") != 4 || managementCount(t, p, "idempotency_record") != 4 {
		t.Fatal("replay produced side effects")
	}
	if _, e = store.MutateIndexer(ctx, a, managementUpdate(deleted), "update-deleted", managementHash); !errors.Is(e, ports.ErrNotFound) {
		t.Fatal("tombstone updated", e)
	}
	managementNoSecrets(t, p, created, updated, cleared, deleted)
	if _, e = postgres.Rollback(ctx, dsn, "../../../migrations"); e == nil {
		t.Fatal("management history downgrade allowed")
	}
}

func TestIndexerManagementTailRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			p, _, secrets, a, store := managementFixture(t)
			ctx := t.Context()
			q := db.New(p)
			create := managementCreate(t, secrets)
			configurationExec(t, p, `CREATE FUNCTION management_tail_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private-passkey'; END $$`)
			install := func() {
				configurationExec(t, p, "CREATE TRIGGER management_tail_fail BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION management_tail_fail()")
			}
			remove := func() { configurationExec(t, p, "DROP TRIGGER management_tail_fail ON "+table) }
			install()
			if _, e := store.MutateIndexer(ctx, a, create, "failed-create", managementHash); e != ports.ErrUnavailable {
				t.Fatal("raw tail error leaked", e)
			}
			for _, table := range []string{"indexer", "indexer_configuration", "indexer_lifecycle", "audit_log", "outbox_event", "idempotency_record"} {
				if managementCount(t, p, table) != 0 {
					t.Fatal("create rollback incomplete", table)
				}
			}
			remove()
			created := managementMust(t, store, a, create, "successful-create")
			old, e := q.GetIndexerByID(ctx, created.Indexer.ID)
			if e != nil {
				t.Fatal(e)
			}
			oldConfig, e := q.GetIndexerConfiguration(ctx, old.ID)
			if e != nil {
				t.Fatal(e)
			}
			install()
			for _, m := range []ports.IndexerMutation{managementUpdate(created), {Action: ports.IndexerDelete, IndexerID: old.ID, ExpectedVersion: 1}} {
				if _, e := store.MutateIndexer(ctx, a, m, "failed-"+string(m.Action), managementHash); e != ports.ErrUnavailable {
					t.Fatal("tail error", e)
				}
				row, e := q.GetIndexerByID(ctx, old.ID)
				if e != nil || !reflect.DeepEqual(row, old) {
					t.Fatal("base rollback incomplete", e)
				}
				config, e := q.GetIndexerConfiguration(ctx, old.ID)
				if e != nil || !reflect.DeepEqual(config, oldConfig) {
					t.Fatal("extension rollback incomplete", e)
				}
				available, e := postgres.IndexerAvailableForExecution(ctx, q, old.ID)
				if e != nil || !available {
					t.Fatal("lifecycle rollback incomplete", e)
				}
				for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
					if managementCount(t, p, table) != 1 {
						t.Fatal("tail rollback incomplete", table)
					}
				}
			}
			remove()
			// The failed command key is reusable after rollback.
			managementMust(t, store, a, managementUpdate(created), "failed-update")
		})
	}
}

// Observe a real blocked account-lock query before committing the role change.
func managementAwaitAccountWait(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		e := p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%SELECT id,is_active,role FROM app_user%')`).Scan(&waiting)
		if e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("account lock wait not observed")
		case <-ticker.C:
		}
	}
}
func TestIndexerManagementCurrentAdminPG(t *testing.T) {
	for _, disable := range []bool{false, true} {
		for _, list := range []bool{false, true} {
			name := "demote"
			if disable {
				name = "disable"
			}
			if list {
				name += "-list"
			} else {
				name += "-replay"
			}
			t.Run(name, func(t *testing.T) {
				p, _, secrets, a, store := managementFixture(t)
				ctx := t.Context()
				create := managementCreate(t, secrets)
				managementMust(t, store, a, create, "original-command")
				tx, e := p.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
				sql := `UPDATE app_user SET role='member' WHERE id=$1`
				if disable {
					sql = `UPDATE app_user SET is_active=false WHERE id=$1`
				}
				if _, e = tx.Exec(ctx, sql, a.ID); e != nil {
					t.Fatal(e)
				}
				result := make(chan error, 1)
				go func() {
					if list {
						_, e := store.ListManagedIndexers(ctx, a)
						result <- e
					} else {
						_, e := store.MutateIndexer(ctx, a, create, "original-command", managementHash)
						result <- e
					}
				}()
				managementAwaitAccountWait(t, p)
				if e = tx.Commit(ctx); e != nil {
					t.Fatal(e)
				}
				select {
				case e = <-result:
					if !errors.Is(e, ports.ErrForbidden) {
						t.Fatal("stale admin authorized", e)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("blocked repository did not finish")
				}
				if managementCount(t, p, "idempotency_record") != 1 {
					t.Fatal("unauthorized command effects")
				}
			})
		}
	}
	// Token, member and invalid actors are rejected before opening a transaction.
	store := postgres.NewIndexerManagementStore(nil, nil, nil)
	for _, a := range []*ports.Actor{nil, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin, Scopes: []string{}}, {Type: ports.ActorTypeSystem, ID: uuid.NewString(), Role: ports.RoleAdmin}} {
		_, e := store.ListManagedIndexers(t.Context(), a)
		if e != ports.ErrUnauthorized && e != ports.ErrForbidden {
			t.Fatal("actor accepted", e)
		}
	}
}

func TestIndexerManagementSchemaAndSnapshotPG(t *testing.T) {
	p, dsn, secrets, a, store := managementFixture(t)
	ctx := t.Context()
	q := db.New(p)
	// An empty 85 migration can roundtrip.
	if v, e := postgres.Rollback(ctx, dsn, "../../../migrations"); e != nil || v != 84 {
		t.Fatal("empty downgrade failed", v, e)
	}
	if available, e := postgres.IndexerAvailableForExecution(ctx, q, uuid.New()); e != nil || !available {
		t.Fatal("historical fixture probe failed", e)
	}
	if _, e := postgres.Migrate(ctx, dsn, "../../../migrations"); e != nil {
		t.Fatal(e)
	}
	created := managementMust(t, store, a, managementCreate(t, secrets), "schema-create")
	// The guard rejects content changes at the old version and mismatched advances.
	for _, sql := range []string{`UPDATE indexer_configuration SET protocol='rss' WHERE indexer_id=$1`, `UPDATE indexer_configuration SET url_encrypted='x'::bytea WHERE indexer_id=$1`, `UPDATE indexer_configuration SET behavior_version='legacy-v1' WHERE indexer_id=$1`, `UPDATE indexer_configuration SET configuration_version=2 WHERE indexer_id=$1`} {
		if _, e := p.Exec(ctx, sql, created.Indexer.ID); e == nil {
			t.Fatal("version guard accepted invalid update")
		}
	}
	configurationExec(t, p, `DELETE FROM indexer_lifecycle WHERE indexer_id=$1`, created.Indexer.ID)
	if available, e := postgres.IndexerAvailableForExecution(ctx, q, created.Indexer.ID); e != nil || !available {
		t.Fatal("missing legacy lifecycle failed", e)
	}
	configurationExec(t, p, `INSERT INTO indexer_lifecycle(indexer_id) VALUES($1)`, created.Indexer.ID)
	tx, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, e = tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); e != nil {
		t.Fatal(e)
	}
	snapshot := db.New(tx)
	if available, e := postgres.IndexerAvailableForExecution(ctx, snapshot, created.Indexer.ID); e != nil || !available {
		t.Fatal(e)
	}
	configurationExec(t, p, `UPDATE indexer_lifecycle SET enabled=false WHERE indexer_id=$1`, created.Indexer.ID)
	if available, e := postgres.IndexerAvailableForExecution(ctx, snapshot, created.Indexer.ID); e != nil || !available {
		t.Fatal("helper escaped caller snapshot", e)
	}
	if available, e := postgres.IndexerAvailableForExecution(ctx, q, created.Indexer.ID); e != nil || available {
		t.Fatal("disabled lifecycle accepted", e)
	}
}

func TestIndexerManagementReceiptValidationPG(t *testing.T) {
	for _, field := range []string{"hash", "code", "resource", "action", "unsafe-url", "unknown-field"} {
		t.Run(field, func(t *testing.T) {
			p, _, secrets, a, store := managementFixture(t)
			ctx := t.Context()
			m := managementCreate(t, secrets)
			created := managementMust(t, store, a, m, "receipt-command")
			var sql string
			switch field {
			case "hash":
				sql = `UPDATE idempotency_record SET request_hash='changed'`
			case "code":
				sql = `UPDATE idempotency_record SET response_code=202`
			case "resource":
				sql = `UPDATE idempotency_record SET resource_id='changed'`
			case "action":
				sql = `UPDATE idempotency_record SET response_body=jsonb_set(response_body,'{Action}','"delete"')`
			case "unsafe-url":
				sql = `UPDATE idempotency_record SET response_body=jsonb_set(response_body,'{Indexer,URL}','"https://site.test/private-path?passkey=private-passkey"')`
			case "unknown-field":
				sql = `UPDATE idempotency_record SET response_body=response_body || '{"secret":"private-passkey"}'::jsonb`
			}
			configurationExec(t, p, sql)
			if field != "hash" {
				if _, e := store.ReadIndexerManagementReceipt(ctx, a, ports.IndexerCreate, "receipt-command"); !errors.Is(e, ports.ErrIndexerCommandConflict) {
					t.Fatal("reader accepted unsafe receipt", e)
				}
			}
			if _, e := store.MutateIndexer(ctx, a, m, "receipt-command", managementHash); !errors.Is(e, ports.ErrIndexerCommandConflict) {
				t.Fatal("invalid receipt accepted", e)
			}
			if managementCount(t, p, "indexer") != 1 || managementCount(t, p, "outbox_event") != 1 {
				t.Fatal("rejected replay had effects")
			}
			if created.Indexer.ID == uuid.Nil {
				t.Fatal("missing ID")
			}
		})
	}
}

func TestIndexerManagementReadReceiptPG(t *testing.T) {
	p, _, secrets, a, store := managementFixture(t)
	ctx := t.Context()
	m := managementCreate(t, secrets)
	created := managementMust(t, store, a, m, "read-create")
	updated := managementMust(t, store, a, managementUpdate(created), "read-update")
	del := ports.IndexerMutation{Action: ports.IndexerDelete, IndexerID: created.Indexer.ID, ExpectedVersion: 2}
	deleted := managementMust(t, store, a, del, "read-delete")
	for _, x := range []struct {
		key string
		r   *ports.IndexerManagementReceipt
	}{{"read-create", created}, {"read-update", updated}, {"read-delete", deleted}} {
		got, e := store.ReadIndexerManagementReceipt(ctx, a, x.r.Action, x.key)
		if e != nil || !reflect.DeepEqual(got, x.r) {
			t.Fatal("receipt read changed historical result", e)
		}
	}
	if _, e := store.ReadIndexerManagementReceipt(ctx, a, ports.IndexerCreate, "missing-command"); !errors.Is(e, ports.ErrNotFound) {
		t.Fatal("missing receipt result", e)
	}
	if _, e := store.ReadIndexerManagementReceipt(ctx, a, ports.IndexerUpdate, "read-create"); !errors.Is(e, ports.ErrNotFound) {
		t.Fatal("wrong namespace result", e)
	}
	if _, e := store.ReadIndexerManagementReceipt(ctx, a, "other", "read-create"); !errors.Is(e, ports.ErrRejected) {
		t.Fatal("invalid action accepted", e)
	}
	tx, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, e = tx.Exec(ctx, `UPDATE app_user SET role='member' WHERE id=$1`, a.ID); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		_, e := store.ReadIndexerManagementReceipt(ctx, a, ports.IndexerCreate, "read-create")
		result <- e
	}()
	managementAwaitAccountWait(t, p)
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-result:
		if !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("demoted reader accepted", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader stuck")
	}
	if managementCount(t, p, "outbox_event") != 3 || managementCount(t, p, "idempotency_record") != 3 {
		t.Fatal("read produced writes")
	}
}

func TestIndexerManagementFrozenHistoryPG(t *testing.T) {
	f := frozenUpgrade(t)
	m5UpgradeSchema83(t, f)
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{41}, 32))
	m5UpgradeSource(t, f, codec)
	f.enable(t)
	command := f.command(t)
	reserved, e := f.store.ReserveUpgrade(t.Context(), f.actor, command, "management-frozen")
	if e != nil || reserved == nil {
		t.Fatal("freeze reservation", e)
	}
	before := m5UpgradeFrozenRows(t, f)
	// These three tables receive NEW management rows; all historical business
	// rows and authority snapshots must remain byte-for-byte unchanged.
	delete(before, "audit_log")
	delete(before, "outbox_event")
	delete(before, "idempotency_record")
	var oldReceipt string
	if e = f.p.QueryRow(t.Context(), `SELECT response_body::text FROM idempotency_record WHERE idempotency_key='upgrade.reserve:management-frozen'`).Scan(&oldReceipt); e != nil {
		t.Fatal(e)
	}
	if _, e = postgres.Migrate(t.Context(), f.dsn, "../../../migrations"); e != nil {
		t.Fatal(e)
	}
	if e = postgres.EncryptLegacyIndexerConfigurations(t.Context(), f.p, codec, search.ClassifyLegacyIndexerProtocol); e != nil {
		t.Fatal(e)
	}
	row, e := db.New(f.p).GetIndexerByID(t.Context(), f.source)
	if e != nil {
		t.Fatal(e)
	}
	store := postgres.NewIndexerManagementStore(f.p, managementIDs{}, codec)
	managementMust(t, store, f.actor, ports.IndexerMutation{Action: ports.IndexerDelete, IndexerID: f.source, ExpectedVersion: row.ConfigVersion}, "delete-frozen-source")
	m5UpgradeAssertFrozen(t, f, before)
	var currentReceipt string
	if e = f.p.QueryRow(t.Context(), `SELECT response_body::text FROM idempotency_record WHERE idempotency_key='upgrade.reserve:management-frozen'`).Scan(&currentReceipt); e != nil || currentReceipt != oldReceipt {
		t.Fatal("historical receipt changed", e)
	}
	if _, e = db.New(f.p).GetCandidateByID(t.Context(), f.candidate); e != nil {
		t.Fatal("candidate FK history lost", e)
	}
	f.store = postgres.NewUpgradeReservationStore(f.p, idgen.New(), codec)
	replay, e := f.store.ReserveUpgrade(t.Context(), f.actor, command, "management-frozen")
	if e != nil || replay == nil || !reflect.DeepEqual(replay, reserved) {
		t.Fatal("protected historical reservation replay failed", e)
	}
	available, e := postgres.IndexerAvailableForExecution(t.Context(), db.New(f.p), f.source)
	if e != nil || available {
		t.Fatal("deleted protected source executable", e)
	}
}

func TestIndexerManagementConfigurationValidationPG(t *testing.T) {
	p, _, secrets, a, store := managementFixture(t)
	ctx := t.Context()
	create := managementCreate(t, secrets)
	for _, kind := range []string{"path-origin", "mismatch-origin", "bad-url-envelope", "bad-credential-envelope", "nil-categories", "invalid-category"} {
		m := create
		m.Configuration.Categories = append([]int{}, create.Configuration.Categories...)
		switch kind {
		case "path-origin":
			m.Configuration.DisplayURL = "https://site.test/private-path"
		case "mismatch-origin":
			m.Configuration.DisplayURL = "https://other.test"
		case "bad-url-envelope":
			m.Configuration.URLCiphertext = []byte("private-passkey")
		case "bad-credential-envelope":
			m.Configuration.CredentialCiphertext = []byte("private-credential")
		case "nil-categories":
			m.Configuration.Categories = nil
		case "invalid-category":
			m.Configuration.Categories = []int{0}
		}
		if _, e := store.MutateIndexer(ctx, a, m, "invalid-"+kind, managementHash); e != ports.ErrRejected && e != ports.ErrUnavailable {
			t.Fatal("invalid configuration accepted", kind, e)
		}
	}
	if managementCount(t, p, "indexer") != 0 || managementCount(t, p, "idempotency_record") != 0 {
		t.Fatal("invalid configuration wrote rows")
	}
	created := managementMust(t, store, a, create, "category-create")
	if !reflect.DeepEqual(created.Indexer.Categories, []int{2000}) {
		t.Fatal("create categories lost")
	}
	listed, e := store.ListManagedIndexers(ctx, a)
	if e != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0].Categories, []int{2000}) {
		t.Fatal("list categories lost", e)
	}
	listed[0].Categories[0] = 999
	created.Indexer.Categories[0] = 998
	read, e := store.ReadIndexerManagementReceipt(ctx, a, ports.IndexerCreate, "category-create")
	if e != nil || !reflect.DeepEqual(read.Indexer.Categories, []int{2000}) {
		t.Fatal("return slice escaped into storage", e)
	}
	update := managementUpdate(read)
	update.PreserveURL = false
	update.Configuration.DisplayURL = "https://replacement.test"
	update.Configuration.URLCiphertext, e = secrets.EncryptSecret([]byte("https://replacement.test/private-path?passkey=private-passkey"))
	if e != nil {
		t.Fatal(e)
	}
	updated := managementMust(t, store, a, update, "replace-url")
	if updated.Indexer.Categories == nil || len(updated.Indexer.Categories) != 0 || updated.Indexer.URL != "https://replacement.test" {
		t.Fatal("update categories or origin mismatch")
	}
	row, e := db.New(p).GetIndexerByID(ctx, updated.Indexer.ID)
	if e != nil {
		t.Fatal(e)
	}
	resolved, _, e := postgres.ResolveIndexerConfiguration(ctx, db.New(p), secrets, row)
	if e != nil || !strings.HasPrefix(resolved.Url, "https://replacement.test/private-path?") {
		t.Fatal("replacement URL did not resolve", e)
	}
	list, e := store.ListManagedIndexers(ctx, a)
	if e != nil || len(list) != 1 || list[0].Categories == nil || len(list[0].Categories) != 0 {
		t.Fatal("empty categories not preserved", e)
	}
	managementNoSecrets(t, p, read, updated)
}
