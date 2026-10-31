package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func cloudIsolationFixture(t *testing.T, count int, corrupt func(int) bool) (cloudScheduleFixture, []*ports.Actor) {
	t.Helper()
	f := newCloudScheduleFixture(t)
	if version, err := postgres.Migrate(t.Context(), f.dsn, "../../../migrations"); err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatal("current migration", version, err)
	}
	owners := []*ports.Actor{f.owner}
	for i := 1; i < count; i++ {
		owners = append(owners, f.user(t, fmt.Sprintf("isolated-%d", i)))
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].ID < owners[j].ID })
	raw, err := json.Marshal(cookieConfigInput().Connection)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	for i, a := range owners {
		cipher, err := f.secret.EncryptSecret(raw)
		if err != nil {
			t.Fatal(err)
		}
		if corrupt(i) {
			cipher[len(cipher)-1] ^= 1
		}
		f.exec(t, `INSERT INTO cookiecloud_configuration(owner_id,version,enabled,interval_seconds,server_origin,connection_encrypted,created_at,updated_at) VALUES($1,1,true,60,'https://cloud.example',$2,'2000-01-01','2000-01-01')`, a.ID, cipher)
	}
	return f, owners
}

func cloudQueueGraph(t *testing.T, f cloudScheduleFixture, want int64) {
	t.Helper()
	for _, table := range []string{"job", "cookiecloud_sync_attempt", "cookiecloud_sync_schedule", "audit_log", "outbox_event"} {
		if got := f.count(t, table); got != want {
			t.Fatalf("%s=%d want=%d", table, got, want)
		}
	}
}

func TestCookieCloudScheduleIsolatesOwnerFailurePG(t *testing.T) {
	for _, mode := range []string{"ciphertext", "tail_write"} {
		t.Run(mode, func(t *testing.T) {
			f, owners := cloudIsolationFixture(t, 3, func(i int) bool { return mode == "ciphertext" && i == 1 })
			if mode == "tail_write" {
				f.exec(t, fmt.Sprintf(`CREATE FUNCTION cloud_owner_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.aggregate_type='cookiecloud.sync' AND NEW.aggregate_id='%s' THEN RAISE EXCEPTION 'fixture-private-cloud'; END IF; RETURN NEW; END $$; CREATE TRIGGER cloud_owner_fault BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION cloud_owner_fault()`, owners[1].ID))
			}
			for range 2 {
				err := f.runtime.EnqueueDue(t.Context())
				if !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "fixture-private") {
					t.Fatal("failure lost or exposed", err)
				}
				cloudQueueGraph(t, f, 2)
				var attempts int
				if err = f.p.QueryRow(t.Context(), `SELECT count(*) FROM cookiecloud_sync_attempt WHERE owner_id=$1`, owners[1].ID).Scan(&attempts); err != nil || attempts != 0 {
					t.Fatal("failed owner partially committed", attempts, err)
				}
			}
			if mode == "tail_write" {
				f.exec(t, "DROP TRIGGER cloud_owner_fault ON outbox_event")
				f.enqueue(t)
				cloudQueueGraph(t, f, 3)
			}
		})
	}
}

func TestCookieCloudScheduleFailedPrefixPG(t *testing.T) {
	f, _ := cloudIsolationFixture(t, 66, func(i int) bool { return i < 64 })
	if err := f.runtime.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
	cloudQueueGraph(t, f, 0)
	f.runtime = postgres.NewCookieCloudSyncStore(f.p, idgen.New(), f.secret, f.secret)
	if err := f.runtime.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
	f.enqueue(t)
	cloudQueueGraph(t, f, 2)
	// An older due time must be picked up when the scan wraps, independently of UUID order.
	actor := f.user(t, "wrapped-owner")
	raw, err := json.Marshal(cookieConfigInput().Connection)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	cipher, err := f.secret.EncryptSecret(raw)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO cookiecloud_configuration(owner_id,version,enabled,interval_seconds,server_origin,connection_encrypted,created_at,updated_at) VALUES($1,1,true,60,'https://cloud.example',$2,'1999-01-01','1999-01-01')`, actor.ID, cipher)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.runtime.EnqueueDue(t.Context()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(err)
		}
	}
	cloudQueueGraph(t, f, 3)
}

func TestCookieCloudScheduleBusyAndCancellationPG(t *testing.T) {
	f, owners := cloudIsolationFixture(t, 3, func(int) bool { return false })
	blocker, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(t.Context(), `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, owners[1].ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() { defer close(done); result <- f.runtime.EnqueueDue(ctx) }()
	defer func() { cancel(); _ = blocker.Rollback(context.Background()); <-done }()
	waitPreparationBlock(t, f.p, blocker.Conn().PgConn().PID())
	busy, stopBusy := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer stopBusy()
	if err = f.runtime.EnqueueDue(busy); err != nil {
		t.Fatal("busy scanner blocked", err)
	}
	stopped, stop := context.WithCancel(t.Context())
	stop()
	if err = f.runtime.EnqueueDue(stopped); !errors.Is(err, context.Canceled) {
		t.Fatal("busy cancellation lost", err)
	}
	cancel()
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("scan cancellation lost", err)
	}
	cloudQueueGraph(t, f, 1)
	if err = blocker.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.enqueue(t)
	cloudQueueGraph(t, f, 3)
}

func TestCookieCloudScheduleIndependentScannersPG(t *testing.T) {
	f, _ := cloudIsolationFixture(t, 3, func(int) bool { return false })
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		scanner := postgres.NewCookieCloudSyncStore(f.p, idgen.New(), f.secret, f.secret)
		wg.Add(1)
		go func() { defer wg.Done(); errs <- scanner.EnqueueDue(t.Context()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("independent scanner failed", err)
		}
	}
	cloudQueueGraph(t, f, 3)
}
