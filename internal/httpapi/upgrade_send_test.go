package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func publicSendBody(t *testing.T, f *publicPreparationFixture) string {
	t.Helper()
	w := f.prepared(t, "public-send-preparation")
	var p ports.MovieUpgradePreparationResult
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"operation_id": p.OperationID, "task_id": p.TaskID, "confirmed": true})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
func assertPublicSendQueue(t *testing.T, f *publicPreparationFixture, first *httptest.ResponseRecorder) {
	t.Helper()
	if first.Code != 202 {
		t.Fatalf("send status %d %s", first.Code, first.Body.String())
	}
	var receipt ports.MovieUpgradeSendJobReceipt
	if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.OperationID != f.operation || receipt.JobID == uuid.Nil || receipt.TaskID == uuid.Nil || receipt.Status != "queued" || receipt.ExecutionAllowed {
		t.Fatal("unsafe send receipt")
	}
	var count int
	if err := f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_send_job)+(SELECT count(*) FROM job WHERE kind='movie_upgrade_native_send')`).Scan(&count); err != nil || count != 2 {
		t.Fatal("not exactly one send command/job", count, err)
	}
	if f.fetches.Load() != 1 || f.calls.Load() != 0 {
		t.Fatal("HTTP command executed network")
	}
	var physical int
	if err := f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_send_authorization)+(SELECT count(*) FROM movie_upgrade_send_dispatch)+(SELECT count(*) FROM movie_upgrade_send_result)`).Scan(&physical); err != nil || physical != 0 {
		t.Fatal("HTTP command executed", physical, err)
	}
}
func TestPublicUpgradeSendCookieOriginalKeyPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	body := publicSendBody(t, f)
	files := f.unchangedFiles(t)
	wrong := func(r *http.Request) {
		r.URL.Path = strings.Replace(r.URL.Path, f.request.SubscriptionID.String(), uuid.NewString(), 1)
	}
	if w := f.command("upgrade-sends", body, "original-send", f.admin, wrong); w.Code != 409 {
		t.Fatalf("wrong initial subscription %d", w.Code)
	}
	const n = 8
	results := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = f.command("upgrade-sends", body, "original-send", f.admin, nil) }()
	}
	wg.Wait()
	for _, w := range results {
		assertPublicSendQueue(t, f, w)
		if w.Body.String() != results[0].Body.String() {
			t.Fatal("receipt changed")
		}
	}
	if w := f.command("upgrade-sends", body, "original-send", f.admin, wrong); w.Code != 409 {
		t.Fatalf("wrong recovery subscription %d", w.Code)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	m["task_id"] = uuid.New()
	b, _ := json.Marshal(m)
	changed := string(b)
	if w := f.command("upgrade-sends", changed, "original-send", f.admin, nil); w.Code != 409 {
		t.Fatalf("changed task %d", w.Code)
	}
	if w := f.command("upgrade-sends", body, "another-send", f.admin, nil); w.Code != 409 {
		t.Fatalf("second send %d", w.Code)
	}
	if files != f.unchangedFiles(t) {
		t.Fatal("send command changed original file/facts")
	}
}
func TestPublicUpgradeSendStrictAdmissionPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	body := publicSendBody(t, f)
	tokens := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.a.DBPool(), idgen.New()), crypto.ReadTokenIssuer{}, f.a.Clock())
	_, secret, err := tokens.Create(context.Background(), f.actor, ports.TokenCommand{Name: "send read"}, "send-read")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, body, cookie string
		edit               func(*http.Request)
		want               int
	}{
		{"anonymous", body, "", nil, 401}, {"member", body, f.member, nil, 403}, {"token", body, "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }, 403},
		{"csrf", body, f.admin, func(r *http.Request) { r.Header.Set("Origin", "https://foreign.invalid") }, 403},
		{"duplicate-key", body, f.admin, func(r *http.Request) { r.Header.Add("Idempotency-Key", "second") }, 400}, {"missing-key", body, f.admin, func(r *http.Request) { r.Header.Del("Idempotency-Key") }, 400},
		{"duplicate-field", strings.TrimSuffix(body, "}") + `,"confirmed":true}`, f.admin, nil, 400}, {"case-alias", strings.Replace(body, "task_id", "Task_id", 1), f.admin, nil, 400},
		{"unicode-alias", strings.Replace(body, "confirmed", "cоnfirmed", 1), f.admin, nil, 400}, {"unknown", strings.TrimSuffix(body, "}") + `,"owner":"caller"}`, f.admin, nil, 400},
		{"false-confirm", strings.Replace(body, "true", "false", 1), f.admin, nil, 400}, {"null", strings.Replace(body, `"task_id":"`, `"task_id":null,"unused":"`, 1), f.admin, nil, 400},
		{"trailing", body + " {}", f.admin, nil, 400}, {"large", strings.Repeat(" ", 65537), f.admin, nil, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := f.command("upgrade-sends", c.body, "admit-"+c.name, c.cookie, c.edit)
			if w.Code != c.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	f.noSend(t, 1)
}
func TestPublicUpgradeSendCurrentEvidencePG(t *testing.T) {
	for _, kind := range []string{"intent", "client", "skip"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublicPreparationFixture(t)
			body := publicSendBody(t, f)
			switch kind {
			case "intent":
				f.exec(t, "UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1", f.request.SubscriptionID)
			case "client":
				f.exec(t, "UPDATE download_client SET url=url||'/changed' WHERE id=$1", f.client)
			case "skip":
				f.exec(t, "UPDATE wanted_item SET is_skipped=true WHERE subscription_id=$1", f.request.SubscriptionID)
			}
			w := f.command("upgrade-sends", body, "changed-send", f.admin, nil)
			if w.Code != 409 {
				t.Fatalf("stale send %d %s", w.Code, w.Body.String())
			}
			f.noSend(t, 1)
		})
	}
}
func TestPublicUpgradeSendRevokedDuringWaitPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	body := publicSendBody(t, f)
	first := f.command("upgrade-sends", body, "revoked-send", f.admin, nil)
	assertPublicSendQueue(t, f, first)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := f.a.DBPool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", uuid.MustParse(f.actor.ID)); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.command("upgrade-sends", body, "revoked-send", f.admin, nil) }()
	selectionWaitFor(t, func() bool {
		var waiting bool
		_ = f.a.DBPool().QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%app_user%')").Scan(&waiting)
		return waiting
	})
	if _, err = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != 403 {
			t.Fatalf("revoked read %d", w.Code)
		}
	case <-ctx.Done():
		t.Fatal("revoked receipt did not finish")
	}
}
