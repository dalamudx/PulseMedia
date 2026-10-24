package app

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestM11WebPushChromePG(t *testing.T) {
	if os.Getenv("PULSE_M11_PUSH_BROWSER") != "1" {
		t.Skip("explicit native browser fixture")
	}
	assets, shots := os.Getenv("PULSE_M11_PUSH_ASSETS"), os.Getenv("PULSE_M11_PUSH_SHOTS")
	for _, path := range []string{assets, shots, os.TempDir()} {
		real, err := filepath.EvalSymlinks(path)
		if err != nil || real != filepath.Clean(path) || !strings.HasPrefix(path, "/data/temp/") {
			t.Fatal("owned paths required")
		}
	}
	f := organizationSetup(t)
	ctx := t.Context()
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WebPush = config.WebPushConfig{Enabled: true, PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), Subject: "mailto:owned-push@example.org"}
	application, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	secrets := crypto.NewSecretProtector(outboxTestKey)
	application.indexerDecoder = secrets
	ob := outbox.New(f.pool, f.gen, application.clock)
	application.enableWebPush(ob)
	service := notify.NewWebPushService(postgres.NewWebPushStore(f.pool, f.gen, ob), secrets, cfg.WebPush.PublicKey, true)
	authentication := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	_, session, err := authentication.SetupAdmin(ctx, "push-admin", "Owned-Push-Admin-123!")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := crypto.HashPassword("Owned-Push-Member-123!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "push-member", PasswordHash: hash, Role: ports.RoleMember, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewServerHandler(application, authentication, nil, nil, nil, nil, nil, nil, nil, nil, f.mgr, nil, outboxTestKey)
	handler.SetWebPushService(service)
	router := httpapi.NewRouter(application, handler, authentication, nil)
	file, err := f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: f.root, RelPath: "OwnedBrowser.mkv", SizeBytes: 17})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.target, "OwnedBrowser.mkv"), []byte("owned media bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = f.q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: f.unit}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: f.unit, Stage: "imported", RootID: pgtype.UUID{Bytes: f.root, Valid: true}, SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p"}`)}); err != nil {
		t.Fatal(err)
	}
	control := uuid.NewString()
	files := http.FileServer(http.Dir(assets))
	var mu sync.Mutex
	writes, sends := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/owned/send" {
			if r.Method != http.MethodPost || r.Header.Get("X-Owned-Control") != control {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			tx, e := f.pool.Begin(r.Context())
			if e != nil {
				t.Error(e)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			event, e := ob.PublishImported(r.Context(), f.q.WithTx(tx), f.unit.String(), map[string]any{"unit_id": f.unit, "file_id": file.ID})
			if e == nil {
				e = tx.Commit(r.Context())
			}
			if e != nil {
				t.Error(e)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			consumers, e := f.q.ListRuntimeOutboxConsumers(r.Context())
			if e != nil {
				t.Error(e)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			count := 0
			for _, consumer := range consumers {
				if !strings.HasPrefix(consumer, "webpush:") {
					continue
				}
				device, e := uuid.Parse(strings.TrimPrefix(consumer, "webpush:"))
				if e != nil {
					t.Error(e)
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				d, e := ob.Claim(r.Context(), consumer, "owned-native-browser", time.Minute)
				if e != nil || d == nil {
					t.Error("native browser claim failed", e)
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if e = application.processWebPushDelivery(r.Context(), ob, d.ID, device, "owned-native-browser", d.LeaseToken); e != nil {
					t.Error("native browser send failed", e)
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				stored, e := f.q.GetOutboxDeliveryByID(r.Context(), d.ID)
				if e != nil || stored.State != "delivered" {
					t.Error("FCM did not accept native push", e)
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				count++
			}
			if count != 1 {
				t.Error("expected one original device", count)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			sends++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"event_id": event.ID, "accepted": true})
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if r.URL.Path == "/pulse-push-sw.js" || strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "no-store")
				files.ServeHTTP(w, r)
			} else {
				http.ServeFile(w, r, filepath.Join(assets, "index.html"))
			}
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/web-push/") {
			router.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		before, e := m6DashboardSnapshot(r.Context(), f)
		if e != nil {
			t.Error(e)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		record := httptest.NewRecorder()
		router.ServeHTTP(record, r)
		after, e := m6DashboardSnapshot(r.Context(), f)
		if e != nil {
			t.Error(e)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		allowed := map[string]bool{}
		if r.Method != http.MethodGet {
			writes++
			if record.Code == http.StatusOK || record.Code == http.StatusCreated {
				for _, table := range []string{"web_push_device", "audit_log", "outbox_event", "idempotency_record", "outbox_aggregate_sequence"} {
					allowed[table] = true
				}
			}
		}
		for table, digest := range before {
			if !allowed[table] && after[table] != digest {
				t.Error("push API changed protected table", table)
			}
		}
		for k, values := range record.Header() {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(record.Code)
		_, _ = w.Write(record.Body.Bytes())
	}))
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	setup, _ := json.Marshal(map[string]any{"base": server.URL, "admin": session, "control": control})
	command := exec.CommandContext(browserCtx, "node", "tests/web-push-browser.mjs")
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_M11_PUSH_SETUP="+string(setup), "PULSE_M11_PUSH_CDP="+cdp)
	command.WaitDelay = 10 * time.Second
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output, os.Stdout)
	command.Stderr = command.Stdout
	if err = command.Run(); err != nil {
		t.Fatalf("native push Chrome: %v\n%s", err, output.String())
	}
	if strings.Count(output.String(), "M11_PUSH_CHROME_PASS") != 1 || writes != 1 || sends != 1 {
		t.Fatal("unexpected native browser receipt, writes or send count", writes, sends)
	}
	fmt.Printf("M11_PUSH_SCREENSHOTS_READY dir=%s\n", shots)
	if os.Getenv("PULSE_M11_PUSH_REVIEW") == "1" {
		timer := time.NewTimer(600 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err = os.Stat(filepath.Join(shots, "inspected")); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("review cancelled")
			case <-timer.C:
				t.Fatal("screenshot review absent")
			case <-ticker.C:
			}
		}
	}
	t.Log("M11_PUSH_BROWSER_PG_PASS Cookie=REAL subscription=NATIVE_FCM server=NATIVE_VAPID browser=REAL_NOTIFICATION original_key=READ_ONLY protected=UNCHANGED")
}
