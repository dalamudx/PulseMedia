package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

func TestDeliveryHistoryPaginationAndScope(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(h *httpapi.ServerHandler, a *app.App) {
		h.SetDeliveryManagementService(notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(a.DBPool(), idgen.New())))
	})
	ctx := t.Context()
	q := a.Queries()
	channel := uuid.New()
	if _, e := a.DBPool().Exec(ctx, `INSERT INTO notification_channel(id,name,channel_type,config,config_encrypted,is_enabled) VALUES($1,'History fixture','webhook','{}',decode('0001','hex'),true)`, channel); e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	insert := func(id uuid.UUID, consumer string, old bool) {
		t.Helper()
		ev, e := q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: uuid.New(), AggregateType: "media", AggregateID: uuid.NewString(), EventType: "media.imported", Seq: 1, Payload: []byte(`{"fixture-secret":"private"}`)})
		if e != nil {
			t.Fatal(e)
		}
		d, e := q.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: id, EventID: ev.ID, Consumer: consumer, AggregateType: ev.AggregateType, AggregateID: ev.AggregateID, Seq: 1, MaxAttempts: 3})
		if e != nil {
			t.Fatal(e)
		}
		state := "pending"
		attempts := 0
		if old {
			state = "dead"
			attempts = 3
		}
		if _, e = a.DBPool().Exec(ctx, `UPDATE outbox_delivery SET updated_at=$2,state=$3,attempts=$4,error='fixture-secret' WHERE id=$1`, d.ID, at, state, attempts); e != nil {
			t.Fatal(e)
		}
	}
	ids := make([]uuid.UUID, 121)
	for i := range ids {
		ids[i] = uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1))
		consumer := "notify:" + channel.String()
		if i == 120 {
			consumer = "mediaserver.refresh:" + uuid.NewString()
		}
		insert(ids[i], consumer, i == 0)
	}
	call := func(token, path string, body []byte, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, bytes.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: token})
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	type page struct {
		Items []httpapi.DeliverySummary `json:"items"`
		Limit int                       `json:"limit"`
		Next  string                    `json:"next_cursor"`
	}
	read := func(query string) page {
		t.Helper()
		w := call(admin, "/api/v1/integrations/deliveries"+query, nil, "")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" || bytes.Contains(w.Body.Bytes(), []byte("fixture-secret")) {
			t.Fatalf("unsafe list status=%d", w.Code)
		}
		var p page
		if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
			t.Fatal(e)
		}
		return p
	}
	graph := func() string {
		t.Helper()
		var value string
		e := a.DBPool().QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM outbox_delivery d),(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM outbox_event e),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM idempotency_record))::text`).Scan(&value)
		if e != nil {
			t.Fatal(e)
		}
		return value
	}
	before := graph()
	first := read("")
	if len(first.Items) != 100 || first.Limit != 100 || first.Next == "" {
		t.Fatal("first page", len(first.Items), first.Limit)
	}
	if first.Items[0].Id != ids[120] || first.Items[99].Id != ids[21] {
		t.Fatal("timestamp tie order")
	}
	second := read("?cursor=" + url.QueryEscape(first.Next))
	if len(second.Items) != 21 || second.Next != "" || second.Items[20].Id != ids[0] {
		t.Fatal("older deliveries unreachable", len(second.Items))
	}
	dead := read("?state=dead&limit=1")
	if len(dead.Items) != 1 || dead.Items[0].Id != ids[0] || dead.Next != "" {
		t.Fatal("filter must precede LIMIT")
	}
	delivering := read("?state=delivering")
	if len(delivering.Items) != 0 {
		t.Fatal("wrong state")
	}
	oldEvent := dead.Items[0].EventId.String()
	located := read("?event_id=" + oldEvent)
	if len(located.Items) != 1 || located.Items[0].Id != ids[0] {
		t.Fatal("event filter must precede first 100", len(located.Items))
	}
	if p := read("?event_id=" + oldEvent + "&state=pending"); len(p.Items) != 0 {
		t.Fatal("event and state filters must intersect")
	}
	if p := read("?event_id=" + uuid.NewString()); len(p.Items) != 0 {
		t.Fatal("unknown event exposed unrelated delivery")
	}
	for _, auth := range []struct {
		cookie string
		want   int
	}{{"", 401}, {member, 403}} {
		w := call(auth.cookie, "/api/v1/integrations/deliveries?event_id="+oldEvent, nil, "")
		if w.Code != auth.want || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("private event scope", w.Code, w.Header())
		}
	}
	if before != graph() {
		t.Fatal("list wrote business state")
	}
	for _, query := range []string{"?cursor=" + url.QueryEscape(first.Next) + "&state=dead", "?state=invalid", "?state=dead&state=pending", "?cursor=%zz", "?limit=0", "?limit=101", "?unknown=1", "?cursor=" + string(bytes.Repeat([]byte{'A'}, 513))} {
		if w := call(admin, "/api/v1/integrations/deliveries"+query, nil, ""); w.Code != 400 {
			t.Fatalf("invalid query accepted status=%d", w.Code)
		}
	}
	if w := call(admin, "/api/v1/integrations/deliveries", []byte("{}"), ""); w.Code != 400 {
		t.Fatal("GET body", w.Code)
	}
	if w := call(admin, "/api/v1/integrations/deliveries", nil, "read-only"); w.Code != 400 {
		t.Fatal("GET command key", w.Code)
	}
	if w := call(member, "/api/v1/integrations/deliveries?state=dead", nil, ""); w.Code != 403 {
		t.Fatal("member", w.Code)
	}
	if w := call("", "/api/v1/integrations/deliveries", nil, ""); w.Code != 401 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("unauthenticated response must remain private", w.Code)
	}
	for _, query := range []string{"?event_id=bad", "?event_id=", "?event_id=00000000-0000-0000-0000-000000000000", "?event_id=" + oldEvent + "&event_id=" + oldEvent, "?event_id=" + oldEvent + "&cursor=" + url.QueryEscape(first.Next)} {
		if w := call(admin, "/api/v1/integrations/deliveries"+query, nil, ""); w.Code != 400 {
			t.Fatal("invalid event scope accepted", w.Code)
		}
	}
	original, e := q.GetOutboxDeliveryByID(ctx, ids[0])
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if _, e := q.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: uuid.New(), EventID: original.EventID, Consumer: "notify:" + uuid.NewString(), AggregateType: original.AggregateType, AggregateID: original.AggregateID, Seq: original.Seq, MaxAttempts: 3}); e != nil {
			t.Fatal(e)
		}
	}
	pageEvent := read("?event_id=" + oldEvent + "&limit=1")
	if pageEvent.Next == "" || len(pageEvent.Items) != 1 {
		t.Fatal("scoped continuation missing")
	}
	for _, query := range []string{"?cursor=" + url.QueryEscape(pageEvent.Next), "?event_id=" + uuid.NewString() + "&cursor=" + url.QueryEscape(pageEvent.Next), "?event_id=" + oldEvent + "&state=dead&cursor=" + url.QueryEscape(pageEvent.Next)} {
		if w := call(admin, "/api/v1/integrations/deliveries"+query, nil, ""); w.Code != 400 {
			t.Fatal("scoped cursor crossed range", w.Code)
		}
	}
	seen := map[uuid.UUID]bool{}
	for i := 0; i < 4; i++ {
		for _, item := range pageEvent.Items {
			if item.EventId != original.EventID || seen[item.Id] {
				t.Fatal("event page overlap or leak")
			}
			seen[item.Id] = true
		}
		if pageEvent.Next == "" {
			break
		}
		pageEvent = read("?event_id=" + oldEvent + "&limit=1&cursor=" + url.QueryEscape(pageEvent.Next))
	}
	if len(seen) != 3 || !seen[ids[0]] {
		t.Fatal("original event pagination incomplete", len(seen))
	}
	// The cursor retains its timestamp and ID when its old row moves to a new position.
	if _, e := a.DBPool().Exec(ctx, `UPDATE outbox_delivery SET updated_at=$2 WHERE id=$1`, first.Items[99].Id, at.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	newer := uuid.New()
	insert(newer, "notify:"+channel.String(), false)
	if _, e := a.DBPool().Exec(ctx, `UPDATE outbox_delivery SET updated_at=$2 WHERE id=$1`, newer, at.Add(2*time.Hour)); e != nil {
		t.Fatal(e)
	}
	after := read("?cursor=" + url.QueryEscape(first.Next))
	if !reflect.DeepEqual(after.Items, second.Items) {
		t.Fatal("insertion or cursor-row update changed older page")
	}
	d := dead.Items[0]
	body, _ := json.Marshal(map[string]any{"expected_token": d.LeaseToken, "expected_attempts": d.Attempts, "expected_updated_at": d.UpdatedAt, "accept_duplicate_risk": true})
	replay := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/integrations/deliveries/"+d.Id.String()+"/replay", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+admin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "older-delivery-replay")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	sent := replay()
	if sent.Code != 200 {
		t.Fatal("old delivery replay", sent.Code)
	}
	again := replay()
	if again.Code != 200 || !bytes.Equal(sent.Body.Bytes(), again.Body.Bytes()) {
		t.Fatal("original-key replay changed")
	}
	saved, e := q.GetOutboxDeliveryByID(ctx, d.Id)
	if e != nil || saved.State != "pending" || saved.Attempts != 0 {
		t.Fatal("original delivery was not requeued", e)
	}
	if _, e = a.DBPool().Exec(ctx, `UPDATE app_user SET role='member' WHERE username='admin'`); e != nil {
		t.Fatal(e)
	}
	if w := call(admin, "/api/v1/integrations/deliveries?event_id="+oldEvent+"&cursor="+url.QueryEscape(pageEvent.Next), nil, ""); w.Code != 403 {
		t.Fatal("current revocation", w.Code)
	}
}
