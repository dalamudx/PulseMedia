package httpapi_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type publicPreparationFixture struct {
	*publicUpgradeFixture
	operation uuid.UUID
	raw       []byte
	fetches   atomic.Int32
	onFetch   func()
}

func newPublicPreparationFixture(t *testing.T, beforeReserve ...func(*publicPreparationFixture)) *publicPreparationFixture {
	t.Helper()
	return newPublicPreparationFixtureWithMode(t, ports.UpgradeStopAtCutoff, 1000, beforeReserve...)
}

func newPublicPreparationFixtureWithMode(t *testing.T, mode ports.UpgradeMode, cutoff int, beforeReserve ...func(*publicPreparationFixture)) *publicPreparationFixture {
	t.Helper()
	f := &publicPreparationFixture{}
	f.publicUpgradeFixture = newPublicUpgradeFixtureWithCutoff(t, cutoff, func(h *httpapi.ServerHandler, a *app.App) {
		master, err := crypto.EnsureMasterKey(a.Config().DataDir, a.Config().SecretKey)
		if err != nil {
			t.Fatal(err)
		}
		repo, err := postgres.NewUpgradePreparationStore(a.DBPool(), idgen.New(), torrent.Inspector{})
		if err != nil {
			t.Fatal(err)
		}
		inspector, err := downloadstore.NewMovieUpgradePreparationInspector(master, torznab.CompleteTVFetcher{}, torrent.Inspector{})
		if err != nil {
			t.Fatal(err)
		}
		service, err := acquisition.NewPublicUpgradePreparationService(repo, inspector, idgen.New())
		if err != nil {
			t.Fatal(err)
		}
		h.SetPublicUpgradePreparationService(service)
		commands, err := postgres.NewMovieUpgradeSendCommandRepository(repo)
		if err != nil {
			t.Fatal(err)
		}
		send, err := acquisition.NewPublicUpgradeSendService(commands)
		if err != nil {
			t.Fatal(err)
		}
		h.SetPublicUpgradeSendService(send)
	})
	name := "Fight.Club.1999.2160p.Bluray.x265.mkv"
	info := fmt.Sprintf("d6:lengthi120e4:name%d:%s12:piece lengthi16384e6:pieces20:01234567890123456789e", len(name), name)
	announce := "https://private.invalid/announce?passkey=not-for-output"
	f.raw = []byte(fmt.Sprintf("d8:announce%d:%s4:info%se", len(announce), announce, info))
	hash := sha1.Sum([]byte(info))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		if r.URL.Path != "/torrent" || r.URL.Query().Get("apikey") != "private-source-key" {
			w.WriteHeader(404)
			return
		}
		if f.onFetch != nil {
			f.onFetch()
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		if _, err := w.Write(f.raw); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	master, err := crypto.EnsureMasterKey(f.a.Config().DataDir, f.a.Config().SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.Encrypt(master, []byte("private-source-key"))
	if err != nil {
		t.Fatal(err)
	}
	link, err := crypto.Encrypt(master, []byte(server.URL+"/torrent?apikey=private-source-key"))
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, "UPDATE indexer SET url=$2,api_key_encrypted=$3,health='online',health_next_check_at=clock_timestamp()+interval '1 hour' WHERE id=$1", f.source, server.URL, key)
	f.exec(t, "UPDATE release_candidate SET info_hash=$2,link_encrypted=$3 WHERE id=$1", f.request.CandidateID, hex.EncodeToString(hash[:]), link)
	f.exec(t, "INSERT INTO media_identity(id,media_id,namespace,external_id) SELECT $1,media_id,'tmdb.movie','550' FROM subscription WHERE id=$2", uuid.New(), f.request.SubscriptionID)
	f.enable(t, mode)
	for _, configure := range beforeReserve {
		configure(f)
	}
	response := f.command("upgrade-reservations", f.reservationBody(t), "preparation-reserve", f.admin, nil)
	if response.Code != 202 {
		t.Fatalf("reserve %d %s", response.Code, response.Body.String())
	}
	var result ports.UpgradeReservationResult
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	f.operation = result.OperationID
	return f
}
func (f *publicPreparationFixture) body() string {
	return fmt.Sprintf(`{"operation_id":%q,"confirmed":true}`, f.operation)
}
func (f *publicPreparationFixture) prepared(t *testing.T, key string) *httptest.ResponseRecorder {
	t.Helper()
	w := f.command("upgrade-preparations", f.body(), key, f.admin, nil)
	if w.Code != 202 {
		var leases, preparations int
		_ = f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_preparation_lease),(SELECT count(*) FROM movie_upgrade_preparation)`).Scan(&leases, &preparations)
		t.Fatalf("prepare %d leases=%d preparations=%d fetches=%d %s", w.Code, leases, preparations, f.fetches.Load(), w.Body.String())
	}
	return w
}
func (f *publicPreparationFixture) noSend(t *testing.T, tasks int) {
	t.Helper()
	var got, other int
	if err := f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM download_task),(SELECT count(*) FROM job)+(SELECT count(*) FROM movie_upgrade_send_authorization)+(SELECT count(*) FROM movie_upgrade_send_dispatch)+(SELECT count(*) FROM movie_upgrade_send_result)+(SELECT count(*) FROM movie_upgrade_send_job)`).Scan(&got, &other); err != nil {
		t.Fatal(err)
	}
	if got != tasks || other != 0 || f.calls.Load() != 0 {
		t.Fatalf("tasks=%d forbidden=%d downloader calls=%d", got, other, f.calls.Load())
	}
}
func TestPublicUpgradePreparationCookieRecoveryPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	before := f.unchangedFiles(t)
	first := f.prepared(t, "original-preparation")
	var result ports.MovieUpgradePreparationResult
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OperationID != f.operation || result.TaskID == uuid.Nil || result.Status != "prepared_not_sent" || result.ExecutionAllowed || len(result.RawSHA256) != 64 {
		t.Fatalf("invalid result %+v", result)
	}
	f.exec(t, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", f.source)
	f.exec(t, "UPDATE download_client SET url='http://127.0.0.1:1/changed' WHERE id=$1", f.client)
	f.exec(t, "UPDATE library_root SET health='offline' WHERE id=$1", f.root)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			w := f.command("upgrade-preparations", f.body(), "original-preparation", f.admin, nil)
			if w.Code != 202 || w.Body.String() != first.Body.String() {
				t.Errorf("recovery %d %s", w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	if w := f.command("upgrade-preparations", f.body(), "different-key", f.admin, nil); w.Code != 409 {
		t.Fatalf("new key %d", w.Code)
	}
	if f.fetches.Load() != 1 || before != f.unchangedFiles(t) {
		t.Fatal("recovery refetched or changed original files")
	}
	for _, secret := range []string{"not-for-output", "private-source-key", "private-upgrade-password", "owner", "token", "lease_until", "snapshot", "original.mkv"} {
		if strings.Contains(first.Body.String(), secret) {
			t.Fatal("private material in receipt")
		}
	}
	f.noSend(t, 1)
}
func TestPublicUpgradePreparationStrictAdmissionPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	issuer := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.a.DBPool(), idgen.New()), crypto.ReadTokenIssuer{}, f.a.Clock())
	_, secret, err := issuer.Create(context.Background(), f.actor, ports.TokenCommand{Name: "prep read"}, "prep-read-token")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, body, cookie string
		edit               func(*http.Request)
	}{
		{"missing confirmation", fmt.Sprintf(`{"operation_id":%q}`, f.operation), f.admin, nil},
		{"false confirmation", fmt.Sprintf(`{"operation_id":%q,"confirmed":false}`, f.operation), f.admin, nil},
		{"duplicate", strings.Replace(f.body(), `"confirmed":true`, `"confirmed":true,"confirmed":true`, 1), f.admin, nil},
		{"unknown proof", strings.TrimSuffix(f.body(), "}") + `,"proof":{}}`, f.admin, nil},
		{"unicode alias", strings.Replace(f.body(), "operation_id", "operatİon_id", 1), f.admin, nil},
		{"null operation", `{"operation_id":null,"confirmed":true}`, f.admin, nil},
		{"trailing JSON", f.body() + "{}", f.admin, nil},
		{"missing key", f.body(), f.admin, func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
		{"duplicate key", f.body(), f.admin, func(r *http.Request) { r.Header.Add("Idempotency-Key", "second") }},
		{"cross origin", f.body(), f.admin, func(r *http.Request) { r.Header.Set("Origin", "https://outside.invalid") }},
		{"anonymous", f.body(), "", nil},
		{"member", f.body(), f.member, nil},
		{"API token", f.body(), "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := f.command("upgrade-preparations", c.body, "strict-preparation", c.cookie, c.edit)
			if w.Code < 400 || w.Code >= 500 {
				t.Fatalf("status %d %s", w.Code, w.Body.String())
			}
		})
	}
	var leases int
	if err = f.a.DBPool().QueryRow(context.Background(), "SELECT count(*) FROM movie_upgrade_preparation_lease").Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 || f.fetches.Load() != 0 {
		t.Fatal("invalid command reached claim or fetch")
	}
	f.noSend(t, 0)
}
func TestPublicUpgradePreparationChangedSourceRollbackPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	before := f.unchangedFiles(t)
	f.onFetch = func() { f.exec(t, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", f.source) }
	w := f.command("upgrade-preparations", f.body(), "changed-source", f.admin, nil)
	if w.Code != 409 {
		t.Fatalf("changed source %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := f.a.DBPool().QueryRow(context.Background(), "SELECT count(*) FROM movie_upgrade_preparation").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 || before != f.unchangedFiles(t) || f.fetches.Load() != 1 {
		t.Fatal("changed source produced preparation or facts")
	}
	f.noSend(t, 0)
}
func TestPublicUpgradePreparationForgedReceiptPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	f.prepared(t, "forged-receipt")
	f.exec(t, `UPDATE idempotency_record SET response_body=jsonb_set(response_body,'{execution_allowed}','true') WHERE idempotency_key='upgrade.prepare:forged-receipt'`)
	w := f.command("upgrade-preparations", f.body(), "forged-receipt", f.admin, nil)
	if w.Code != 409 {
		t.Fatalf("forged receipt %d %s", w.Code, w.Body.String())
	}
	if f.fetches.Load() != 1 {
		t.Fatal("forged receipt triggered refetch")
	}
	f.noSend(t, 1)
}

// publicPreparationStoreForPool centralizes native proof validation in the
// existing integration fixture; HTTP tests consume its repository contract.
func publicPreparationStoreForPool(t *testing.T, pool *pgxpool.Pool) *postgres.UpgradePreparationStore {
	t.Helper()
	store, err := postgres.NewUpgradePreparationStore(pool, idgen.New(), torrent.Inspector{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}
