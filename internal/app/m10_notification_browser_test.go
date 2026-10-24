package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

func TestM10NotificationChromePG(t *testing.T) {
	if os.Getenv("PULSE_M10_NOTIFICATION_BROWSER") != "1" {
		t.Skip("explicit browser fixture")
	}
	assets, shots := os.Getenv("PULSE_M10_NOTIFICATION_ASSETS"), os.Getenv("PULSE_M10_NOTIFICATION_SHOTS")
	for _, path := range []string{assets, shots, os.TempDir()} {
		real, e := filepath.EvalSymlinks(path)
		if e != nil || real != filepath.Clean(path) || !filepath.IsAbs(path) || !strings.HasPrefix(path, "/data/temp/") {
			t.Fatal("owned paths required")
		}
	}
	f := organizationSetup(t)
	ctx := t.Context()
	application, e := New(config.NewDefault(), WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = application.Close() })
	authentication := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	admin, adminSession, e := authentication.SetupAdmin(ctx, "notification-admin", "Owned-Notification-Admin-123!")
	if e != nil {
		t.Fatal(e)
	}
	memberID := uuid.New()
	hash, e := crypto.HashPassword("Owned-Notification-Member-123!")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateUser(ctx, db.CreateUserParams{ID: memberID, Username: "notification-member", PasswordHash: hash, Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	_, memberSession, e := authentication.Login(ctx, "notification-member", "Owned-Notification-Member-123!")
	if e != nil {
		t.Fatal(e)
	}
	ob, _, dispatcher := testOutboxServices(f)
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID.String(), Role: ports.RoleAdmin}
	old, e := dispatcher.CreateChannel(ctx, actor, "Owned WeChat", "wechat", notify.ChannelConfig{URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=owned-private", ImageURL: "https://image.tmdb.org/t/p/w342/owned.jpg"}, "owned-seed")
	if e != nil {
		t.Fatal(e)
	}
	handler := httpapi.NewServerHandler(application, authentication, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, outboxTestKey)
	handler.SetNotificationDispatcher(dispatcher)
	handler.SetNotificationTestService(notify.NewTestService(postgres.NewNotificationTestStore(f.pool, f.gen, ob)))
	handler.SetChannelStateService(notify.NewChannelStateService(postgres.NewChannelStateStore(f.pool, f.gen)))
	handler.SetChannelReplacementService(notify.NewChannelReplacementService(postgres.NewChannelReplacementStore(f.pool, f.gen), crypto.NewSecretProtector(outboxTestKey)))
	handler.SetDeliveryManagementService(notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(f.pool, f.gen)))
	handler.SetBatchQueryService(notify.NewBatchQueryService(postgres.NewNotificationBatchQueryStore(f.pool)))
	router := httpapi.NewRouter(application, handler, authentication, nil)
	baseline, e := m6DashboardSnapshot(ctx, f)
	if e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	writes := 0
	files := http.FileServer(http.Dir(assets))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			mu.Lock()
			defer mu.Unlock()
			snapshotCtx, stopSnapshot := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
			defer stopSnapshot()
			before, e := m6DashboardSnapshot(snapshotCtx, f)
			if e != nil || !reflect.DeepEqual(before, baseline) {
				t.Error("unexpected mutation between browser requests")
				http.Error(w, "snapshot", 500)
				return
			}
			record := httptest.NewRecorder()
			router.ServeHTTP(record, r)
			after, e := m6DashboardSnapshot(snapshotCtx, f)
			if e != nil {
				t.Error(e)
				http.Error(w, "snapshot", 500)
				return
			}
			allowed := map[string]bool{}
			mutation := r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/notification-channels")
			if mutation {
				writes++
				if record.Code >= 200 && record.Code < 300 {
					for _, table := range []string{"notification_channel", "outbox_event", "outbox_delivery", "outbox_aggregate_sequence", "audit_log", "idempotency_record"} {
						allowed[table] = true
					}
				}
			}
			for table, digest := range before {
				if !allowed[table] && after[table] != digest {
					t.Errorf("browser changed protected %s", table)
				}
			}
			baseline = after
			for key, values := range record.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(record.Code)
			_, _ = w.Write(record.Body.Bytes())
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	setup, _ := json.Marshal(map[string]any{"base": server.URL, "admin": adminSession, "member": memberSession, "owner": admin.ID, "channel": old.ID})
	cmd := exec.CommandContext(browserCtx, "node", "tests/m10-notification-browser.mjs")
	cmd.Dir = "../../web"
	cmd.Env = append(os.Environ(), "PULSE_M10_NOTIFICATION_SETUP="+string(setup), "PULSE_M10_NOTIFICATION_CDP="+cdp)
	cmd.WaitDelay = 10 * time.Second
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, os.Stdout)
	cmd.Stderr = cmd.Stdout
	if e = cmd.Run(); e != nil {
		t.Fatalf("notification Chrome: %v\n%s", e, output.String())
	}
	if strings.Count(output.String(), "M10_NOTIFICATION_CHROME_PASS") != 1 {
		t.Fatal("browser receipt missing")
	}
	var tests int
	if e = f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_event WHERE event_type='notification.test.v1'`).Scan(&tests); e != nil || tests != 2 {
		t.Fatal("test button duplicate or missing event", tests, e)
	}
	var delivered int
	if e = f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_delivery WHERE state='delivered'`).Scan(&delivered); e != nil || delivered != 0 {
		t.Fatal("UI claimed delivery without native execution", e)
	}
	if writes != 4 {
		t.Fatal("unexpected successful/failed POST count", writes)
	}
	if os.Getenv("PULSE_M10_NOTIFICATION_REVIEW") == "1" {
		fmt.Printf("M10_NOTIFICATION_SCREENSHOTS_READY dir=%s\n", shots)
		deadline := time.NewTimer(300 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			if info, e := os.Stat(filepath.Join(shots, "inspected")); e == nil && info.Mode().IsRegular() {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("review cancelled")
			case <-deadline.C:
				t.Fatal("review missing")
			case <-ticker.C:
			}
		}
	}
	t.Log("M10_NOTIFICATION_COOKIE_CHROME_PASS tests=2 original_key=RESTORED protected=UNCHANGED delivered=ZERO workers=NOT_STARTED")
}
