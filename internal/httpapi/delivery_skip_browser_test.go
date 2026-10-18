package httpapi_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDeliverySkipBrowser(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit browser fixture only")
	}
	a, _, _, _, _, _, router, _ := setupOperationHTTP(t, func(s *httpapi.ServerHandler, a *app.App) {
		s.SetDeliverySkipService(notify.NewDeliverySkipService(postgres.NewDeliveryManagementStore(a.DBPool(), idgen.New())))
		s.SetDeliveryManagementService(notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(a.DBPool(), idgen.New())))
		s.SetBatchQueryService(notify.NewBatchQueryService(postgres.NewNotificationBatchQueryStore(a.DBPool())))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	q := a.Queries()
	ch, e := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: uuid.New(), Name: "Skip Hook", ChannelType: "webhook", ConfigEncrypted: []byte("fixture-only-no-network")})
	if e != nil {
		t.Fatal(e)
	}
	ob := outbox.New(a.DBPool(), idgen.New(), clock.New())
	ids := []string{}
	for _, aggregate := range []string{"first", "conflict"} {
		tx, e := a.DBPool().Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		ev, e := ob.Publish(ctx, q.WithTx(tx), "fixture", aggregate, "notification.fixture", map[string]string{"fixture": "no payload exposed"}, []string{"notify:" + ch.ID.String()})
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		var id string
		if e = a.DBPool().QueryRow(ctx, "UPDATE outbox_delivery SET state='dead',attempts=5 WHERE event_id=$1 RETURNING id::text", ev.ID).Scan(&id); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
	}
	if _, e = q.DisableNotificationChannel(ctx, ch.ID); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	script, e := filepath.Abs("../../web/tests/delivery-skip-browser.mjs")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_UI_FIXTURE=1", "PULSE_SKIP_UI_BASE="+server.URL, "PULSE_SKIP_UI_ID="+ids[0], "PULSE_SKIP_UI_CONFLICT_ID="+ids[1])
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("skip browser failed: %v %s", e, raw)
	}
	t.Log(string(raw))
	var state string
	var delivered bool
	if e = a.DBPool().QueryRow(ctx, "SELECT state,delivered_at IS NOT NULL FROM outbox_delivery WHERE id=$1", ids[0]).Scan(&state, &delivered); e != nil || state != "skipped" || delivered {
		t.Fatal("UI skip manufactured successful delivery", state, e)
	}
}
