package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDeliveryManagementReplayAtomicFenceAndScope(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(handler *httpapi.ServerHandler, a *app.App) {
		handler.SetDeliveryManagementService(notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(a.DBPool(), idgen.New())))
	})
	ctx := context.Background()
	call := func(token, method, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		return out
	}
	channel := uuid.New()
	if _, e := a.DBPool().Exec(ctx, "INSERT INTO notification_channel(id,name,channel_type,config,config_encrypted,is_enabled) VALUES($1,'Fixture webhook','webhook','{}',decode('0001','hex'),true)", channel); e != nil {
		t.Fatal(e)
	}
	event, e := a.Queries().InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: uuid.New(), AggregateType: "media", AggregateID: uuid.NewString(), EventType: "media.imported", Seq: 1, Payload: []byte(`{"fixture-secret":"private"}`)})
	if e != nil {
		t.Fatal(e)
	}
	d, e := a.Queries().InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: uuid.New(), EventID: event.ID, Consumer: "notify:" + channel.String(), AggregateType: event.AggregateType, AggregateID: event.AggregateID, Seq: 1, MaxAttempts: 3})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE outbox_delivery SET state='dead',attempts=3,error='fixture-secret' WHERE id=$1", d.ID); e != nil {
		t.Fatal(e)
	}
	d, e = a.Queries().GetOutboxDeliveryByID(ctx, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	list := call(admin, "GET", "/api/v1/integrations/deliveries", "", nil)
	if list.Code != 200 || bytes.Contains(list.Body.Bytes(), []byte("fixture-secret")) {
		t.Fatal("management list leaked sensitive payload/error", list.Code)
	}
	if out := call(member, "GET", "/api/v1/integrations/deliveries", "", nil); out.Code != 403 {
		t.Fatal("member list scope", out.Code)
	}
	path := "/api/v1/integrations/deliveries/" + d.ID.String() + "/replay"
	body := map[string]any{"expected_token": d.LeaseToken, "expected_attempts": d.Attempts, "expected_updated_at": d.UpdatedAt.Time, "accept_duplicate_risk": true}
	body["accept_duplicate_risk"] = false
	if out := call(admin, "POST", path, "unacknowledged-replay", body); out.Code != 400 {
		t.Fatal("unacknowledged duplicate risk accepted", out.Code)
	}
	body["accept_duplicate_risk"] = true
	disabledBody := map[string]any{"expected_token": d.LeaseToken, "expected_attempts": d.Attempts, "expected_updated_at": d.UpdatedAt.Time, "accept_duplicate_risk": true}
	if _, e = a.DBPool().Exec(ctx, "UPDATE notification_channel SET is_enabled=false WHERE id=$1", channel); e != nil {
		t.Fatal(e)
	}
	if out := call(admin, "POST", path, "disabled-replay", disabledBody); out.Code != 409 {
		t.Fatal("disabled recipient accepted replay", out.Code)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE notification_channel SET is_enabled=true WHERE id=$1", channel); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DBPool().Exec(ctx, "CREATE FUNCTION reject_delivery_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='replay-once' THEN RAISE EXCEPTION 'fixture-secret';END IF;RETURN NEW;END $$;CREATE TRIGGER reject_delivery_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_delivery_receipt()"); e != nil {
		t.Fatal(e)
	}
	fault := call(admin, "POST", path, "replay-once", body)
	if fault.Code != 503 || bytes.Contains(fault.Body.Bytes(), []byte("fixture-secret")) {
		t.Fatal(fault.Code)
	}
	saved, e := a.Queries().GetOutboxDeliveryByID(ctx, d.ID)
	if e != nil || saved.State != "dead" || saved.LeaseToken != d.LeaseToken {
		t.Fatal("failed replay released delivery", saved, e)
	}
	if _, e = a.DBPool().Exec(ctx, "DROP TRIGGER reject_delivery_receipt ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	outputs := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); outputs <- call(admin, "POST", path, "replay-once", body) }()
	}
	wg.Wait()
	close(outputs)
	for out := range outputs {
		if out.Code != 200 || out.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("replay receipt unstable", out.Code)
		}
	}
	saved, e = a.Queries().GetOutboxDeliveryByID(ctx, d.ID)
	if e != nil || saved.State != "pending" || saved.Attempts != 0 || saved.LeaseToken != d.LeaseToken+1 || saved.EventID != event.ID {
		t.Fatal("wrong requeued event/fence", saved, e)
	}
	if out := call(admin, "POST", path, "another-replay", body); out.Code != 409 {
		t.Fatal("stale/dead-only guard failed", out.Code)
	}
	if os.Getenv("PULSE_UI_FIXTURE") == "1" {
		if _, e = a.DBPool().Exec(ctx, "UPDATE outbox_delivery SET state='dead',attempts=3 WHERE id=$1", d.ID); e != nil {
			t.Fatal(e)
		}
		server := httptest.NewServer(router)
		defer server.Close()
		work, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		script, e := filepath.Abs("../../web/tests/delivery-browser.mjs")
		if e != nil {
			t.Fatal(e)
		}
		cmd := exec.CommandContext(work, "node", script)
		cmd.Env = append(os.Environ(), "PULSE_UI_FIXTURE=1", "PULSE_DELIVERY_UI_BASE="+server.URL, "PULSE_DELIVERY_UI_ID="+d.ID.String())
		raw, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("delivery browser failed: %v %s", e, raw)
		}
		t.Log(string(raw))
		saved, e = a.Queries().GetOutboxDeliveryByID(ctx, d.ID)
		if e != nil || saved.State != "pending" || saved.LeaseToken != d.LeaseToken+2 {
			t.Fatal("browser retry performed two physical requeues", saved, e)
		}
	}

	if out := call(member, "POST", path, "replay-once", body); out.Code != 403 {
		t.Fatal("member cached replay elevation", out.Code)
	}
}
