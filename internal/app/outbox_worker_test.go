package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/jellyfin"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppRunOwnsOrganizationAndOutbox(t *testing.T) {
	f := organizationSetup(t)
	_, _, n := testOutboxServices(f)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	createOutboxServer(t, f, server.URL)
	createOutboxChannel(t, f, n, server.URL+"/notify")
	f.observe(t)
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	a, err := New(cfg, WithDBPool(f.pool), WithLogger(logging.NewLogger(io.Discard, "error")))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	done := make(chan struct{})
	var runErr error
	go func() { defer close(done); runErr = a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitOutboxCondition(t, ctx, func() bool {
		var sent int
		err := f.pool.QueryRow(ctx, "SELECT count(*) FROM outbox_delivery WHERE state='delivered'").Scan(&sent)
		return err == nil && sent == 2
	})
	f.assertImported(t)
	if requests.Load() != 2 {
		t.Fatal("production wiring did not use both real adapters", requests.Load())
	}
	cancel()
	<-done
	if runErr != nil {
		t.Fatal(runErr)
	}
	// Close has joined consumers and released the process mutex, not just HTTP.
	restarted, err := New(cfg, WithLogger(logging.NewLogger(io.Discard, "error")))
	if err != nil {
		t.Fatal("shutdown did not release owned runtime", err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxConfigurationIdempotencyInsertFailureRollsBack(t *testing.T) {
	f := organizationSetup(t)
	_, _, n := testOutboxServices(f)
	ctx := context.Background()
	actor := notificationFixtureAdmin(t, f)
	if _, err := f.pool.Exec(ctx, "CREATE FUNCTION fail_channel_idempotency() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER fail_channel_idempotency BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_channel_idempotency()"); err != nil {
		t.Fatal(err)
	}
	cfg := notify.ChannelConfig{URL: "https://example.test", Secret: "private-signing-secret"}
	if _, err := n.CreateChannel(ctx, actor, "Webhook", "webhook", cfg, "channel-key"); err == nil {
		t.Fatal("idempotency failure falsely succeeded")
	}
	for _, table := range []string{"notification_channel", "idempotency_record", "audit_log"} {
		if f.count(t, table) != 0 {
			t.Fatal("partial config command", table)
		}
	}
	if _, err := f.pool.Exec(ctx, "DROP TRIGGER fail_channel_idempotency ON idempotency_record"); err != nil {
		t.Fatal(err)
	}
	ch, err := n.CreateChannel(ctx, actor, "Webhook", "webhook", cfg, "channel-key")
	if err != nil {
		t.Fatal(err)
	}
	again, err := n.CreateChannel(ctx, actor, "Webhook", "webhook", cfg, "channel-key")
	if err != nil || again.ID != ch.ID {
		t.Fatal("command response-loss replay duplicated integration", err)
	}
	if _, err := n.CreateChannel(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: "admin", Role: ports.RoleMember}, "Webhook", "webhook", cfg, "channel-key"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("old idempotency result bypassed current permission", err)
	}
}

var outboxTestKey = make([]byte, 32)

func testOutboxServices(f *organizationFixture) (*outbox.Outbox, *mediaserver.SyncService, *notify.Dispatcher) {
	ob := outbox.New(f.pool, f.gen, clock.New())
	ms := mediaserver.NewSyncService(ob, postgres.NewMediaConfirmationRepository(f.pool, f.gen))
	ms.SetCredentialDecoder(crypto.NewSecretProtector(outboxTestKey))
	ms.SetServerFactory(func(kind, url, key string) (ports.MediaServerClient, error) {
		return jellyfin.NewClient(url, key, nil), nil
	})
	n := notify.NewDispatcher(postgres.NewNotificationChannelReader(f.pool), f.gen, clock.New(), ob, postgres.NewNotificationChannelStore(f.pool, f.gen))
	n.SetClientFactory(crypto.NewSecretProtector(outboxTestKey), notificationClient)
	return ob, ms, n
}
func createOutboxServer(t *testing.T, f *organizationFixture, url string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	cipher, err := crypto.Encrypt(outboxTestKey, []byte("private-server-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	mapping, _ := json.Marshal(map[string]string{f.target: "/movies"})
	if _, err := f.q.CreateMediaServer(context.Background(), db.CreateMediaServerParams{ID: id, Name: "Server", ServerType: "jellyfin", Url: url, ApiKeyEncrypted: cipher, PathMapping: mapping}); err != nil {
		t.Fatal(err)
	}
	return id
}
func notificationFixtureAdmin(t *testing.T, f *organizationFixture) *ports.Actor {
	t.Helper()
	id := uuid.New()
	if _, e := f.q.CreateUser(context.Background(), db.CreateUserParams{ID: id, Username: id.String(), PasswordHash: "test-only-not-for-authentication", Role: ports.RoleAdmin, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	return &ports.Actor{Type: ports.ActorTypeUser, ID: id.String(), Role: ports.RoleAdmin}
}
func createOutboxChannel(t *testing.T, f *organizationFixture, _ *notify.Dispatcher, url string) uuid.UUID {
	t.Helper()
	raw, e := json.Marshal(notify.ChannelConfig{URL: url, Secret: "private-hmac-secret"})
	if e != nil {
		t.Fatal(e)
	}
	cipher, e := crypto.Encrypt(outboxTestKey, raw)
	if e != nil {
		t.Fatal(e)
	}
	row, e := f.q.CreateNotificationChannel(context.Background(), db.CreateNotificationChannelParams{ID: f.gen.NewUUID(), Name: "Webhook", ChannelType: "webhook", ConfigEncrypted: cipher})
	if e != nil {
		t.Fatal(e)
	}
	return row.ID
}
func importForOutbox(t *testing.T, f *organizationFixture) {
	t.Helper()
	f.observe(t)
	if err := f.svc.OrganizeDownload(context.Background(), f.claim(t, "import-worker")); err != nil {
		t.Fatal(err)
	}
	f.assertImported(t)
	sealImportedFixture(t, f)
}

// Direct lease/failure probes need a sealed fixture; App.Run tests use the real fixed window.
func sealImportedFixture(t *testing.T, f *organizationFixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, "UPDATE notification_batch SET state='ready' WHERE state='open'"); err != nil {
		t.Fatal(err)
	}
	ob := outbox.New(f.pool, f.gen, clock.New())
	if _, err := ob.SealDueNotificationBatches(ctx, 32); err != nil {
		t.Fatal(err)
	}
}
func waitOutboxCondition(t *testing.T, ctx context.Context, fn func() bool) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if fn() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("outbox condition deadline")
		case <-tick.C:
		}
	}
}

func TestImportedOutboxRuntimeIndependentRecipients(t *testing.T) {
	f := organizationSetup(t)
	ob, ms, n := testOutboxServices(f)
	var goodRefresh, goodNotify, bad atomic.Int32
	receipts := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/good/Library/Media/Updated":
			if r.Header.Get("Authorization") != `MediaBrowser Token="private-server-api-key"` || !strings.Contains(string(body), "/movies/") || strings.Contains(string(body), f.target) {
				receipts <- "bad refresh credential/path mapping"
			}
			goodRefresh.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "/hook":
			mac := hmac.New(sha256.New, []byte("private-hmac-secret"))
			_, _ = mac.Write(body)
			if r.Header.Get("X-PulseMedia-Event-ID") == "" || r.Header.Get("X-PulseMedia-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || strings.Contains(string(body), f.target) || strings.Contains(string(body), "private-") {
				receipts <- "bad webhook id/signature/privacy"
			}
			goodNotify.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			bad.Add(1)
			http.Error(w, "private-adapter-error-secret", http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	createOutboxServer(t, f, server.URL+"/good")
	createOutboxServer(t, f, server.URL+"/bad")
	createOutboxChannel(t, f, n, server.URL+"/hook")
	createOutboxChannel(t, f, n, server.URL+"/bad-hook")
	importForOutbox(t, f)
	if f.count(t, "outbox_delivery") != 4 {
		t.Fatal("recipients not snapshotted in import transaction")
	}
	a := &App{dbPool: f.pool, idgen: f.gen, logger: logging.NewLogger(io.Discard, "error")}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); a.runOutboxWorkers(ctx, ob, ms, n) }()
	defer func() { cancel(); <-done }()
	waitOutboxCondition(t, ctx, func() bool {
		var delivered, pending int
		err := f.pool.QueryRow(ctx, "SELECT count(*) FILTER(WHERE state='delivered'), count(*) FILTER(WHERE state='pending' AND error IS NOT NULL) FROM outbox_delivery").Scan(&delivered, &pending)
		return err == nil && delivered == 2 && pending == 2
	})
	cancel()
	<-done
	if goodRefresh.Load() != 1 || goodNotify.Load() != 1 || bad.Load() != 2 {
		t.Fatalf("independent actual requests: %d %d %d", goodRefresh.Load(), goodNotify.Load(), bad.Load())
	}
	select {
	case err := <-receipts:
		t.Fatal(err)
	default:
	}
	var leaked bool
	if err := f.pool.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM outbox_delivery WHERE error LIKE '%private-%')").Scan(&leaked); err != nil || leaked {
		t.Fatal("sensitive error persisted", err)
	}
	f.assertImported(t)
}

func TestOutboxCrashAfterSendReplaysStableEventID(t *testing.T) {
	f := organizationSetup(t)
	ob, _, n := testOutboxServices(f)
	var mu sync.Mutex
	var ids, signatures []string
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ids = append(ids, r.Header.Get("X-PulseMedia-Event-ID"))
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, raw)
		signatures = append(signatures, r.Header.Get("X-PulseMedia-Signature"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	channel := createOutboxChannel(t, f, n, server.URL)
	importForOutbox(t, f)
	consumer := "notify:" + channel.String()
	d, err := ob.Claim(context.Background(), consumer, "old-worker", time.Minute)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), "CREATE FUNCTION fail_delivery_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='delivered' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_delivery BEFORE UPDATE ON outbox_delivery FOR EACH ROW EXECUTE FUNCTION fail_delivery_commit()"); err != nil {
		t.Fatal(err)
	}
	if err := n.DispatchDelivery(context.Background(), d.ID, channel, "old-worker", d.LeaseToken); err == nil {
		t.Fatal("send committed despite injected DB failure")
	}
	if _, err := f.pool.Exec(context.Background(), "DROP TRIGGER fail_delivery ON outbox_delivery"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), "UPDATE outbox_delivery SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	if err := n.DispatchDelivery(context.Background(), d.ID, channel, "old-worker", d.LeaseToken); !errors.Is(err, outbox.ErrStaleDeliveryLease) {
		t.Fatal("stale owner sent again", err)
	}
	if count, err := f.q.RecoverExpiredOutboxDeliveries(context.Background(), consumer); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	newOb, _, newN := testOutboxServices(f)
	next, err := newOb.Claim(context.Background(), consumer, "new-worker", time.Minute)
	if err != nil || next == nil || next.LeaseToken <= d.LeaseToken {
		t.Fatal(err)
	}
	if err := newN.DispatchDelivery(context.Background(), next.ID, channel, "new-worker", next.LeaseToken); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] != ids[1] || ids[0] != d.EventID.String() {
		t.Fatalf("at-least-once lost receiver dedupe identity: %v", ids)
	}
	if len(bodies) != 2 || string(bodies[0]) != string(bodies[1]) || signatures[0] != signatures[1] || !strings.Contains(string(bodies[0]), "媒体入库记录") {
		t.Fatal("replay changed frozen Chinese body/HMAC")
	}
	saved, err := f.q.GetOutboxDeliveryByID(context.Background(), d.ID)
	if err != nil || saved.State != "delivered" {
		t.Fatal(saved.State, err)
	}
}

func TestOutboxSendRejectsWrongAuthorityAndRecipient(t *testing.T) {
	f := organizationSetup(t)
	ob, ms, n := testOutboxServices(f)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	ch := createOutboxChannel(t, f, n, server.URL)
	msID := createOutboxServer(t, f, server.URL)
	importForOutbox(t, f)
	nd, _ := ob.Claim(context.Background(), "notify:"+ch.String(), "owner", time.Minute)
	md, _ := ob.Claim(context.Background(), "mediaserver.refresh:"+msID.String(), "owner", time.Minute)
	if nd == nil || md == nil {
		t.Fatal("missing snapshotted recipients")
	}
	for _, p := range []struct {
		owner     string
		token     int64
		recipient uuid.UUID
	}{{"other", nd.LeaseToken, ch}, {"owner", nd.LeaseToken + 1, ch}, {"owner", nd.LeaseToken, uuid.New()}} {
		if err := n.DispatchDelivery(context.Background(), nd.ID, p.recipient, p.owner, p.token); !errors.Is(err, outbox.ErrStaleDeliveryLease) {
			t.Fatal(err)
		}
	}
	if err := ms.ProcessRefreshDelivery(context.Background(), md.ID, uuid.New(), "owner", md.LeaseToken, ""); !errors.Is(err, outbox.ErrStaleDeliveryLease) {
		t.Fatal(err)
	}
	if err := ms.ProcessRefreshDelivery(context.Background(), md.ID, msID, "owner", md.LeaseToken, "/arbitrary/secret.env"); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("unauthorized external I/O")
	}
}

func TestOutboxCommitChecksClockAfterRowLockWait(t *testing.T) {
	for _, kind := range []string{"finish", "fail", "load", "disabled_pause"} {
		t.Run(kind, func(t *testing.T) {
			f := organizationSetup(t)
			ob, _, n := testOutboxServices(f)
			ch := createOutboxChannel(t, f, n, "http://example.test")
			importForOutbox(t, f)
			consumer := "notify:" + ch.String()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			d, err := ob.Claim(ctx, consumer, "owner", 150*time.Millisecond)
			if err != nil || d == nil {
				t.Fatal(err)
			}
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			if _, err := db.New(tx).LockOutboxDelivery(ctx, d.ID); err != nil {
				t.Fatal(err)
			}
			if kind == "disabled_pause" {
				if _, err := f.q.DisableNotificationChannel(ctx, ch); err != nil {
					t.Fatal(err)
				}
			}
			result := make(chan error, 1)
			go func() {
				switch kind {
				case "finish":
					result <- ob.Finish(ctx, d.ID, "owner", d.LeaseToken)
				case "disabled_pause":
					result <- ob.ReturnDisabledNotification(ctx, d.ID, consumer, "owner", d.LeaseToken)
				case "fail":
					result <- ob.Fail(ctx, d.ID, "owner", d.LeaseToken, "safe", time.Second)
				default:
					_, _, err := ob.LoadOwned(ctx, d.ID, consumer, "owner", d.LeaseToken)
					result <- err
				}
				close(result)
			}()
			defer func() { cancel(); _ = tx.Rollback(context.WithoutCancel(ctx)); <-result }()
			waitOutboxCondition(t, ctx, func() bool {
				var expired bool
				err := f.pool.QueryRow(ctx, "SELECT lease_until <= clock_timestamp() FROM outbox_delivery WHERE id=$1", d.ID).Scan(&expired)
				return err == nil && expired
			})
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, outbox.ErrStaleDeliveryLease) {
				t.Fatal("post-wait stale lease accepted", err)
			}
		})
	}
}

func TestImportedOutboxFanoutFailureRollsBackAllBusinessWrites(t *testing.T) {
	f := organizationSetup(t)
	_, _, n := testOutboxServices(f)
	createOutboxChannel(t, f, n, "http://example.test")
	createOutboxServer(t, f, "http://example.test")
	if _, err := f.pool.Exec(context.Background(), "CREATE FUNCTION fail_recipient() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'recipient insert failure'; RETURN NEW; END $$; CREATE TRIGGER fail_recipient BEFORE INSERT ON notification_batch_member FOR EACH ROW EXECUTE FUNCTION fail_recipient()"); err != nil {
		t.Fatal(err)
	}
	f.observe(t)
	lease := f.claim(t, "owner")
	if err := f.svc.OrganizeDownload(context.Background(), lease); err == nil {
		t.Fatal("fanout failure falsely succeeded")
	}
	for _, table := range []string{"library_file", "unit_fact", "outbox_event", "outbox_delivery", "notification_batch", "notification_batch_member"} {
		if f.count(t, table) != 0 {
			t.Fatal("partial import", table)
		}
	}
	var reservations int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM unit_reservation WHERE state='reserved'").Scan(&reservations); err != nil || reservations != 1 {
		t.Fatal("reservation released before fanout commit", reservations, err)
	}
	if _, err := f.pool.Exec(context.Background(), "DROP TRIGGER fail_recipient ON notification_batch_member"); err != nil {
		t.Fatal(err)
	}
	// Model a stopped old process: the physical call returned, then its two authorities expired.
	if _, err := f.pool.Exec(context.Background(), "UPDATE transfer_plan SET lease_until=clock_timestamp()-interval '1 second'; UPDATE job SET lease_until=clock_timestamp()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.RecoverExpiredOrganizationJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.newService().OrganizeDownload(context.Background(), f.claim(t, "restarted")); err != nil {
		t.Fatal(err)
	}
	f.assertImported(t)
	sealImportedFixture(t, f)
	if f.count(t, "outbox_delivery") != 2 {
		t.Fatal("retry duplicated/lost recipient")
	}
}

// Force concurrent same-aggregate publication; count/seq must be assigned under
// a transaction-scoped aggregate lock, not MAX()+1 from independent snapshots.
func TestOutboxConcurrentAggregatePublication(t *testing.T) {
	f := organizationSetup(t)
	ob, _, _ := testOutboxServices(f)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			if _, err = ob.Publish(ctx, db.New(tx), "media", f.unit.String(), "media.imported", map[string]string{}, []string{"notify:fixture"}); err == nil {
				err = tx.Commit(ctx)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var rows, seqs int
	var min, max int64
	if err := f.pool.QueryRow(ctx, "SELECT count(*),count(DISTINCT seq),min(seq),max(seq) FROM outbox_event").Scan(&rows, &seqs, &min, &max); err != nil || rows != 12 || seqs != 12 || min != 1 || max != 12 {
		t.Fatalf("aggregate commit order collision: %d %d %d %d %v", rows, seqs, min, max, err)
	}
}
