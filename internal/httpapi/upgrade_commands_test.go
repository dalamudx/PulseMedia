package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

type publicUpgradeFixture struct {
	*upgradeFixture
	client     uuid.UUID
	calls      atomic.Int32
	runtimeURL string
}

func newPublicUpgradeFixture(t *testing.T, configure ...upgradeFixtureSetup) *publicUpgradeFixture {
	t.Helper()
	return newPublicUpgradeFixtureWithCutoff(t, 1000, configure...)
}

func newPublicUpgradeFixtureWithCutoff(t *testing.T, cutoff int, configure ...upgradeFixtureSetup) *publicUpgradeFixture {
	t.Helper()
	f := &publicUpgradeFixture{upgradeFixture: newUpgradeFixtureWithCutoff(t, cutoff, configure...), client: uuid.New()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.calls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(server.Close)
	master, err := crypto.EnsureMasterKey(f.a.Config().DataDir, f.a.Config().SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := crypto.Encrypt(master, []byte("private-upgrade-password"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.a.Queries().CreateDownloadClient(context.Background(), db.CreateDownloadClientParams{ID: f.client, Name: "Must not be called", ClientType: "qbittorrent", Url: server.URL, PasswordEncrypted: encrypted, RemotePathMap: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, "UPDATE subscription SET downloader_id=$2 WHERE id=$1", f.request.SubscriptionID, f.client)
	f.project(t)
	return f
}
func (f *publicUpgradeFixture) command(route, body, key, cookie string, edit func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/"+f.request.SubscriptionID.String()+"/"+route, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://"+r.Host)
	r.Header.Set("Idempotency-Key", key)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: cookie})
	}
	if edit != nil {
		edit(r)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func (f *publicUpgradeFixture) modeBody(t *testing.T, modes ...ports.UpgradeMode) string {
	t.Helper()
	s, err := f.a.Queries().GetSubscriptionByID(context.Background(), f.request.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	mode := ports.UpgradeStopAtCutoff
	if len(modes) > 0 {
		mode = modes[0]
	}
	b, _ := json.Marshal(map[string]any{"expected_intent": s.IntentVersion, "mode": mode, "confirmed": true})
	return string(b)
}
func (f *publicUpgradeFixture) enable(t *testing.T, modes ...ports.UpgradeMode) string {
	t.Helper()
	body := f.modeBody(t, modes...)
	w := f.command("upgrade-modes", body, "enable-upgrades", f.admin, nil)
	if w.Code != 202 {
		t.Fatalf("mode %d %s", w.Code, w.Body.String())
	}
	f.project(t)
	return body
}
func (f *publicUpgradeFixture) reservationBody(t *testing.T) string {
	t.Helper()
	p := upgradeResult(t, f.call(f.body(), f.admin, nil))
	if !p.Improvement || len(p.ExpectedClientDigest) != 64 {
		t.Fatalf("missing trusted preview observation: %+v", p)
	}
	b, _ := json.Marshal(map[string]any{"current_file_id": f.request.CurrentFileID, "candidate_id": f.request.CandidateID, "expected": p.Version, "expected_client_digest": p.ExpectedClientDigest, "confirmed": true})
	return string(b)
}
func (f *publicUpgradeFixture) unchangedFiles(t *testing.T) string {
	t.Helper()
	var value string
	err := f.a.DBPool().QueryRow(context.Background(), `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(t)) FROM library_file t),(SELECT jsonb_agg(to_jsonb(t)) FROM library_file_unit t),(SELECT jsonb_agg(to_jsonb(t)) FROM unit_fact t))::text`).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func (f *publicUpgradeFixture) noExecution(t *testing.T) {
	t.Helper()
	var count int
	err := f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM job)+(SELECT count(*) FROM download_task)+(SELECT count(*) FROM transfer_plan)+(SELECT count(*) FROM movie_upgrade_send_authorization)+(SELECT count(*) FROM movie_upgrade_send_dispatch)+(SELECT count(*) FROM movie_upgrade_send_result)+(SELECT count(*) FROM movie_upgrade_send_job)`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || f.calls.Load() != 0 {
		t.Fatalf("unexpected execution rows=%d network=%d", count, f.calls.Load())
	}
}
func TestPublicUpgradeCommandsCookieReceiptPG(t *testing.T) {
	f := newPublicUpgradeFixture(t)
	files := f.unchangedFiles(t)
	body := f.reservationBody(t)
	if w := f.command("upgrade-reservations", body, "disabled-reserve", f.admin, nil); w.Code != 409 {
		t.Fatalf("disabled %d %s", w.Code, w.Body.String())
	}
	mode := f.enable(t)
	body = f.reservationBody(t)
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- f.command("upgrade-reservations", body, "lost-reservation", f.admin, nil)
		}()
	}
	wg.Wait()
	close(responses)
	var receipt string
	for w := range responses {
		if w.Code != 202 || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("reserve %d %s", w.Code, w.Body.String())
		}
		if receipt == "" {
			receipt = w.Body.String()
		}
		if receipt != w.Body.String() {
			t.Fatal("same-key receipt changed")
		}
		var result ports.UpgradeReservationResult
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Status != "reserved_not_executable" || result.ExecutionAllowed || result.OperationID == uuid.Nil || result.ReservationID == uuid.Nil {
			t.Fatal("invalid non-executable receipt")
		}
	}
	// Historical mode receipt survives the later reservation and changed intent.
	w := f.command("upgrade-modes", mode, "enable-upgrades", f.admin, nil)
	if w.Code != 202 {
		t.Fatalf("mode replay %d %s", w.Code, w.Body.String())
	}
	changed := strings.Replace(body, f.request.CandidateID.String(), uuid.NewString(), 1)
	if w = f.command("upgrade-reservations", changed, "lost-reservation", f.admin, nil); w.Code != 409 {
		t.Fatalf("changed input %d %s", w.Code, w.Body.String())
	}
	var authorities, reservations int
	if err := f.a.DBPool().QueryRow(context.Background(), "SELECT (SELECT count(*) FROM movie_upgrade_authority),(SELECT count(*) FROM acquisition_reservation)").Scan(&authorities, &reservations); err != nil {
		t.Fatal(err)
	}
	if authorities != 1 || reservations != 1 {
		t.Fatalf("duplicate reservation %d %d", authorities, reservations)
	}
	if files != f.unchangedFiles(t) {
		t.Fatal("reservation changed existing files/facts")
	}
	for _, s := range []string{"private-upgrade-password", "original.mkv", "source_path", "password_encrypted", "snapshot"} {
		if strings.Contains(receipt, s) {
			t.Fatal("private material in receipt")
		}
	}
	f.noExecution(t)
}
func TestPublicUpgradeCommandsStrictAdmissionPG(t *testing.T) {
	f := newPublicUpgradeFixture(t)
	f.enable(t)
	body := f.reservationBody(t)
	mode := f.modeBody(t)
	tokens := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.a.DBPool(), idgen.New()), crypto.ReadTokenIssuer{}, f.a.Clock())
	_, secret, err := tokens.Create(context.Background(), f.actor, ports.TokenCommand{Name: "upgrade read"}, "upgrade-command-read")
	if err != nil {
		t.Fatal(err)
	}
	before := f.state(t)
	cases := []struct {
		name, route, body, cookie string
		edit                      func(*http.Request)
		want                      int
	}{
		{"anonymous", "upgrade-reservations", body, "", nil, 401}, {"member", "upgrade-modes", mode, f.member, nil, 403}, {"token", "upgrade-reservations", body, "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }, 403}, {"csrf", "upgrade-reservations", body, f.admin, func(r *http.Request) { r.Header.Set("Origin", "https://foreign.invalid") }, 403},
		{"duplicate-key", "upgrade-modes", mode, f.admin, func(r *http.Request) { r.Header.Add("Idempotency-Key", "second") }, 400}, {"missing-key", "upgrade-reservations", body, f.admin, func(r *http.Request) { r.Header.Del("Idempotency-Key") }, 400},
		{"duplicate-body", "upgrade-modes", strings.TrimSuffix(mode, "}") + `,"mode":"disabled"}`, f.admin, nil, 400}, {"unicode-alias", "upgrade-modes", strings.Replace(mode, "mode", "mоde", 1), f.admin, nil, 400}, {"unknown", "upgrade-reservations", strings.TrimSuffix(body, "}") + `,"execution_allowed":true}`, f.admin, nil, 400}, {"nested-duplicate", "upgrade-reservations", strings.Replace(body, `"input_stamp":`, `"input_stamp":"fake","input_stamp":`, 1), f.admin, nil, 400}, {"false-confirm", "upgrade-modes", strings.Replace(mode, "true", "false", 1), f.admin, nil, 400}, {"unsupported", "upgrade-modes", strings.Replace(mode, "stop_at_cutoff", "future_mode", 1), f.admin, nil, 400}, {"null", "upgrade-reservations", strings.Replace(body, `"expected":{`, `"expected":null,"extra":{`, 1), f.admin, nil, 400}, {"trailing", "upgrade-reservations", body + " {}", f.admin, nil, 400}, {"large", "upgrade-reservations", strings.Repeat(" ", 65537), f.admin, nil, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := f.command(c.route, c.body, "admission-"+c.name, c.cookie, c.edit)
			if w.Code != c.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	if before != f.state(t) {
		t.Fatal("rejected commands changed persisted state")
	}
	f.noExecution(t)
}
func TestPublicUpgradeCommandsClientAndVersionPG(t *testing.T) {
	for _, kind := range []string{"digest", "version", "expired-source"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublicUpgradeFixture(t)
			f.enable(t)
			body := f.reservationBody(t)
			before := f.unchangedFiles(t)
			switch kind {
			case "digest":
				f.exec(t, "UPDATE download_client SET url=url||'/changed-after-preview' WHERE id=$1", f.client)
			case "version":
				f.exec(t, "UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1", f.request.SubscriptionID)
			case "expired-source":
				f.exec(t, "UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes' WHERE id=$1", f.query)
			}
			w := f.command("upgrade-reservations", body, "stale-reservation", f.admin, nil)
			if w.Code != 409 {
				t.Fatalf("stale %d %s", w.Code, w.Body.String())
			}
			if before != f.unchangedFiles(t) {
				t.Fatal("failed reservation changed facts")
			}
			var count int
			if err := f.a.DBPool().QueryRow(context.Background(), "SELECT count(*) FROM movie_upgrade_authority").Scan(&count); err != nil || count != 0 {
				t.Fatalf("authority leaked %d %v", count, err)
			}
			f.noExecution(t)
		})
	}
}
func TestPublicUpgradeCommandsReplayRevokedDuringWaitPG(t *testing.T) {
	f := newPublicUpgradeFixture(t)
	f.enable(t)
	body := f.reservationBody(t)
	if w := f.command("upgrade-reservations", body, "revoke-replay", f.admin, nil); w.Code != 202 {
		t.Fatalf("initial %d %s", w.Code, w.Body.String())
	}
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
	go func() { done <- f.command("upgrade-reservations", body, "revoke-replay", f.admin, nil) }()
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
			t.Fatalf("revoked replay %d %s", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	f.noExecution(t)
}
