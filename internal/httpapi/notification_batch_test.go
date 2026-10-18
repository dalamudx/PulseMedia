package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotificationBatchHTTPTruthAndPrivacy(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(s *httpapi.ServerHandler, a *app.App) {
		s.SetBatchQueryService(notify.NewBatchQueryService(postgres.NewNotificationBatchQueryStore(a.DBPool())))
		s.SetDeliveryManagementService(notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(a.DBPool(), idgen.New())))
	})
	ctx := context.Background()
	q := a.Queries()
	call := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/integrations/notification-batches", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := call(member); w.Code != 403 {
		t.Fatal("member read private recipient batches", w.Code)
	}
	m, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), Title: "Batch fixture", MediaType: "movie"})
	if e != nil {
		t.Fatal(e)
	}
	u, e := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: uuid.New(), MediaID: m.ID, UnitKey: "movie"})
	if e != nil {
		t.Fatal(e)
	}
	ch, e := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: uuid.New(), Name: "Batch Hook", ChannelType: "webhook", ConfigEncrypted: []byte("fixture-secret-not-for-network")})
	if e != nil {
		t.Fatal(e)
	}
	ob := outbox.New(a.DBPool(), idgen.New(), clock.New())
	for range 2 {
		root, e := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "movie", Name: "private-root-name"})
		if e != nil {
			t.Fatal(e)
		}
		file, e := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: root.ID, RelPath: "private-fixture-path.mkv", SizeBytes: 8192})
		if e != nil {
			t.Fatal(e)
		}
		if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: u.ID}); e != nil {
			t.Fatal(e)
		}
		if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: u.ID, Stage: "imported", RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p"}`)}); e != nil {
			t.Fatal(e)
		}
		tx, e := a.DBPool().Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = ob.PublishImported(ctx, q.WithTx(tx), u.ID.String(), map[string]any{"unit_id": u.ID, "file_id": file.ID, "target_path": "/private-fixture-path"}); e != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	decode := func(w *httptest.ResponseRecorder) []ports.NotificationBatchSummary {
		t.Helper()
		var report struct {
			Items []ports.NotificationBatchSummary `json:"items"`
			Limit int                              `json:"limit"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &report) != nil || report.Limit != 100 || w.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(w.Body.String(), "fixture-secret") || strings.Contains(w.Body.String(), "private-fixture-path") {
			t.Fatal("bad/private observations", w.Code, w.Body.String())
		}
		return report.Items
	}
	items := decode(call(admin))
	if len(items) != 1 || items[0].State != "collecting" || items[0].EventID != nil || items[0].MemberCount != 2 || items[0].UnitCount != 1 || items[0].RootCount != 2 {
		t.Fatal("incorrect open observation", items)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE notification_batch SET state='ready' WHERE state='open'"); e != nil {
		t.Fatal(e)
	}
	items = decode(call(admin))
	if items[0].State != "sealing_pending" {
		t.Fatal("ready was presented as delivered", items)
	}
	if n, e := ob.SealDueNotificationBatches(ctx, 32); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	items = decode(call(admin))
	if items[0].State != "pending" || items[0].EventID == nil {
		t.Fatal("sealed confused with delivered", items)
	}
	if _, e = q.DisableNotificationChannel(ctx, ch.ID); e != nil {
		t.Fatal(e)
	}
	items = decode(call(admin))
	if items[0].RecipientEnabled || items[0].State != "pending" {
		t.Fatal("disabled queue disappeared", items)
	}
	if os.Getenv("PULSE_UI_FIXTURE") == "1" {
		server := httptest.NewServer(router)
		defer server.Close()
		work, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		script, e := filepath.Abs("../../web/tests/notification-batch-browser.mjs")
		if e != nil {
			t.Fatal(e)
		}
		cmd := exec.CommandContext(work, "node", script)
		cmd.Env = append(os.Environ(), "PULSE_UI_FIXTURE=1", "PULSE_BATCH_UI_BASE="+server.URL, "PULSE_BATCH_UI_ID="+items[0].ID.String())
		raw, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("batch browser failed: %v %s", e, raw)
		}
		t.Log(string(raw))
	}
}
