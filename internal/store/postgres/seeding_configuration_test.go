package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	seed "github.com/dalamudx/PulseMedia/internal/usecase/seeding"
)

type seedConfigFixture struct {
	cookieConfigFixture
	cfg    *seed.ConfigService
	client db.DownloadClient
}

func newSeedConfigFixture(t *testing.T) seedConfigFixture {
	t.Helper()
	f := seedConfigFixture{cookieConfigFixture: newCookieConfigFixture(t)}
	f.history = repositoryMigrationsThrough(t, 111)
	if version, err := postgres.Migrate(t.Context(), f.dsn, f.history); err != nil || version != 111 {
		t.Fatal("seeding config schema", version, err)
	}
	encrypted, err := f.secret.EncryptSecret([]byte("owned-native-credential"))
	if err != nil {
		t.Fatal(err)
	}
	f.client, err = db.New(f.p).CreateDownloadClient(t.Context(), db.CreateDownloadClientParams{ID: idgen.New().NewUUID(), Name: "owned seeding client", ClientType: "qbittorrent", Url: "https://seed.example", Username: "owned-user", PasswordEncrypted: encrypted, RemotePathMap: []byte(`[]`)})
	if err != nil {
		t.Fatal("client fixture", err)
	}
	f.cfg = seed.NewConfigService(postgres.NewSeedingConfigStore(f.p, idgen.New()))
	return f
}
func (f seedConfigFixture) input() ports.SeedingConfigInput {
	ratio := 2.0
	return ports.SeedingConfigInput{ExpectedClientUpdatedAt: f.client.UpdatedAt.Time.UTC(), Enabled: true, IntervalSeconds: 1200, Rule: rule.Rule{Mode: rule.MatchAny, MinShareRatio: &ratio}}
}
func (f seedConfigFixture) saveSeed(t *testing.T, in ports.SeedingConfigInput, key string) *ports.SeedingConfigReceipt {
	t.Helper()
	r, err := f.cfg.Save(t.Context(), f.owner, f.client.ID, in, key)
	if err != nil || r == nil {
		t.Fatal("seeding config save", err)
	}
	return r
}
func seedConfigProtected(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch k {
		case "seeding_configuration", "audit_log", "outbox_event", "idempotency_record":
		default:
			out[k] = v
		}
	}
	return out
}
func TestSeedingConfigConcurrentOriginalPG(t *testing.T) {
	f := newSeedConfigFixture(t)
	in := f.input()
	before := f.snapshot(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	out := make([]*ports.SeedingConfigReceipt, 2)
	errs := make([]error, 2)
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out[i], errs[i] = f.cfg.Save(t.Context(), f.owner, f.client.ID, in, "original")
		}(i)
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(out[0], out[1]) {
		t.Fatal("same-key concurrency", errs)
	}
	for _, table := range []string{"seeding_configuration", "audit_log", "outbox_event", "idempotency_record"} {
		if f.count(t, table) != 1 {
			t.Fatal("duplicated transaction", table)
		}
	}
	if !reflect.DeepEqual(seedConfigProtected(before), seedConfigProtected(f.snapshot(t))) {
		t.Fatal("configuration altered business facts or task queue")
	}
	original, _ := json.Marshal(out[0])
	in.ExpectedClientUpdatedAt = in.ExpectedClientUpdatedAt.In(time.FixedZone("owned", 8*3600))
	again, err := f.cfg.Save(t.Context(), f.owner, f.client.ID, in, "original")
	wire, _ := json.Marshal(again)
	if err != nil || !bytes.Equal(wire, original) {
		t.Fatal("equivalent timestamp changed original result", err)
	}
	var stamp string
	if err = f.p.QueryRow(t.Context(), "SELECT client_stamp FROM seeding_configuration WHERE client_id=$1", f.client.ID).Scan(&stamp); err != nil || len(stamp) != 64 {
		t.Fatal("frozen client binding", err)
	}
}
func TestSeedingConfigAccountHistoricalAndOfflinePG(t *testing.T) {
	f := newSeedConfigFixture(t)
	in := f.input()
	original := f.saveSeed(t, in, "original")
	wire, _ := json.Marshal(original)
	if _, err := f.cfg.Receipt(t.Context(), f.other, f.client.ID, "original"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("other account read original", err)
	}
	current, err := f.cfg.Read(t.Context(), f.other, f.client.ID)
	if err != nil || current.OwnerID.String() != f.owner.ID {
		t.Fatal("administrator current configuration", err)
	}
	changed := in
	changed.Enabled = false
	if _, err = f.cfg.Save(t.Context(), f.owner, f.client.ID, changed, "original"); !errors.Is(err, ports.ErrSeedingConfigConflict) {
		t.Fatal("same key changed body", err)
	}
	f.exec(t, "UPDATE download_client SET url='https://offline.invalid',updated_at=updated_at+interval '1 second' WHERE id=$1", f.client.ID)
	before := f.snapshot(t)
	again, err := f.cfg.Save(t.Context(), f.owner, f.client.ID, in, "original")
	got, _ := json.Marshal(again)
	if err != nil || !bytes.Equal(got, wire) {
		t.Fatal("original reread current client", err)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("original recovery wrote")
	}
	if _, err = f.cfg.Save(t.Context(), f.owner, f.client.ID, changed, "stale-client"); !errors.Is(err, ports.ErrSeedingConfigConflict) {
		t.Fatal("stale client accepted", err)
	}
	client, err := db.New(f.p).GetDownloadClientByID(t.Context(), f.client.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed.ExpectedVersion = 1
	changed.ExpectedClientUpdatedAt = client.UpdatedAt.Time
	disabled, err := f.cfg.Save(t.Context(), f.other, f.client.ID, changed, "disable")
	if err != nil || disabled.Config.Enabled || disabled.Config.Version != 2 || disabled.Config.OwnerID.String() != f.other.ID {
		t.Fatal("offline disable", err)
	}
	receipt, err := f.cfg.Receipt(t.Context(), f.owner, f.client.ID, "original")
	got, _ = json.Marshal(receipt)
	if err != nil || !bytes.Equal(got, wire) {
		t.Fatal("new owner changed historical receipt", err)
	}
}
func TestSeedingConfigConcurrentVersionsPG(t *testing.T) {
	f := newSeedConfigFixture(t)
	in := f.input()
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	actors := []*ports.Actor{f.owner, f.other}
	for i := range actors {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = f.cfg.Save(t.Context(), actors[i], f.client.ID, in, fmt.Sprintf("administrator-%d", i))
		}(i)
	}
	close(start)
	wg.Wait()
	success, conflict := 0, 0
	for _, err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ports.ErrSeedingConfigConflict) {
			conflict++
		} else {
			t.Fatal("unexpected concurrent error", err)
		}
	}
	if success != 1 || conflict != 1 || f.count(t, "seeding_configuration") != 1 || f.count(t, "audit_log") != 1 || f.count(t, "idempotency_record") != 1 {
		t.Fatal("concurrent versions not atomic", errs)
	}
}
func TestSeedingConfigTailRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := newSeedConfigFixture(t)
			before := f.snapshot(t)
			f.exec(t, `CREATE FUNCTION seed_config_test_fail() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned-seeding-tail-fault'; END $$`)
			f.exec(t, "CREATE TRIGGER seed_config_test_fail BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION seed_config_test_fail()")
			if _, err := f.cfg.Save(t.Context(), f.owner, f.client.ID, f.input(), "recover"); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("tail failure not safe", err)
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("tail failure left partial transaction")
			}
			f.exec(t, "DROP TRIGGER seed_config_test_fail ON "+table)
			f.exec(t, "DROP FUNCTION seed_config_test_fail()")
			f.saveSeed(t, f.input(), "recover")
		})
	}
}
func TestSeedingConfigDeferredCommitFencesPG(t *testing.T) {
	for _, name := range []string{"permission", "client", "configuration", "receipt"} {
		t.Run(name, func(t *testing.T) {
			f := newSeedConfigFixture(t)
			before := f.snapshot(t)
			statement := ""
			onTable := "audit_log"
			condition := "NEW.action='seeding.configuration'"
			want := ports.ErrSeedingConfigConflict
			switch name {
			case "permission":
				statement = "UPDATE app_user SET role='member' WHERE id=NEW.actor_id::uuid"
				want = ports.ErrForbidden
			case "client":
				statement = "UPDATE download_client SET url='https://tampered.invalid' WHERE id=NEW.target_id::uuid"
			case "configuration":
				statement = "UPDATE seeding_configuration SET version=version+1,interval_seconds=interval_seconds+1,updated_at=clock_timestamp() WHERE client_id=NEW.target_id::uuid"
			case "receipt":
				onTable = "idempotency_record"
				condition = "NEW.idempotency_key LIKE 'seeding.configuration:%'"
				statement = "UPDATE idempotency_record SET response_code=201 WHERE id=NEW.id"
				want = ports.ErrUnavailable
			}
			f.exec(t, "CREATE FUNCTION seed_config_test_defer() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF "+condition+" THEN "+statement+"; END IF; RETURN NEW; END $$")
			f.exec(t, "CREATE CONSTRAINT TRIGGER seed_config_test_defer AFTER INSERT ON "+onTable+" DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION seed_config_test_defer()")
			if _, err := f.cfg.Save(t.Context(), f.owner, f.client.ID, f.input(), "recover"); !errors.Is(err, want) {
				t.Fatal("late mutation committed or misclassified", err)
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("late mutation left partial transaction")
			}
			var role string
			if err := f.p.QueryRow(t.Context(), "SELECT role FROM app_user WHERE id=$1", f.owner.ID).Scan(&role); err != nil || role != ports.RoleAdmin {
				t.Fatal("authority mutation leaked", err)
			}
			f.exec(t, "DROP TRIGGER seed_config_test_defer ON "+onTable)
			f.exec(t, "DROP FUNCTION seed_config_test_defer()")
			f.saveSeed(t, f.input(), "recover")
		})
	}
}
func TestSeedingConfigSchema111HistoryPG(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newSeedConfigFixture(t)
		if v, err := postgres.Rollback(t.Context(), f.dsn, f.history); err != nil || v != 110 {
			t.Fatal("empty down", v, err)
		}
		if v, err := postgres.Migrate(t.Context(), f.dsn, f.history); err != nil || v != 111 {
			t.Fatal("empty reinstall", v, err)
		}
	})
	t.Run("history", func(t *testing.T) {
		f := newSeedConfigFixture(t)
		f.saveSeed(t, f.input(), "history")
		before := f.snapshot(t)
		for _, statement := range []string{"DELETE FROM seeding_configuration", "TRUNCATE seeding_configuration", "UPDATE seeding_configuration SET version=version+2", "UPDATE seeding_configuration SET version=version+1,delete_files=true"} {
			if _, err := f.p.Exec(t.Context(), statement); err == nil {
				t.Fatal("protected history/unsafe removal accepted")
			}
		}
		if _, err := postgres.Rollback(t.Context(), f.dsn, f.history); err == nil {
			t.Fatal("nonempty history downgraded")
		}
		if !reflect.DeepEqual(before, f.snapshot(t)) {
			t.Fatal("failed downgrade changed history")
		}
	})
}
func TestSeedingConfigNoExecutionAndCancellationPG(t *testing.T) {
	f := newSeedConfigFixture(t)
	before := f.snapshot(t)
	if v, err := f.cfg.Read(t.Context(), f.owner, f.client.ID); err != nil || v != nil {
		t.Fatal("absent configuration", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.cfg.Save(ctx, f.owner, f.client.ID, f.input(), "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled save", err)
	}
	invalid := f.input()
	invalid.Rule.DeleteFiles = true
	if _, err := f.cfg.Save(t.Context(), f.owner, f.client.ID, invalid, "delete-data"); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("data deletion policy accepted", err)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("read/rejected/cancelled request wrote")
	}
	f.saveSeed(t, f.input(), "saved")
	if f.count(t, "job") != 0 || f.count(t, "download_task") != 0 || !reflect.DeepEqual(seedConfigProtected(before), seedConfigProtected(f.snapshot(t))) {
		t.Fatal("saving eligibility configuration scheduled execution")
	}
	f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", f.owner.ID)
	before = f.snapshot(t)
	if _, err := f.cfg.Read(t.Context(), f.owner, f.client.ID); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("inactive current read", err)
	}
	if _, err := f.cfg.Receipt(t.Context(), f.owner, f.client.ID, "saved"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("inactive historical read", err)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("revoked reads wrote")
	}
}
