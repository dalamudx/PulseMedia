package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	downloadcase "github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var dedicatedControlScopes = []string{"automatic-tv", "complete-tv", "movie-upgrade"}

type dedicatedControlFixture struct {
	pool                    *pgxpool.Pool
	task                    db.DownloadTask
	actor                   *ports.Actor
	service                 *downloadcase.ControlService
	repo                    *download.Repository
	paused, loseACK, badTag atomic.Bool
	reads, sends            atomic.Int32
	onRead, onSend          atomic.Pointer[func()]
}

type dedicatedControlTransport struct{ target *url.URL }

func (r dedicatedControlTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// Deliberately model a native client that cannot physically cancel an admitted call.
	clone := request.Clone(context.WithoutCancel(request.Context()))
	u := *clone.URL
	u.Scheme = r.target.Scheme
	u.Host = r.target.Host
	clone.URL = &u
	return http.DefaultTransport.RoundTrip(clone)
}

func newDedicatedControl(t *testing.T, scope string) *dedicatedControlFixture {
	t.Helper()
	f := &dedicatedControlFixture{}
	var op uuid.UUID
	var key []byte
	switch scope {
	case "automatic-tv":
		h := newAutomaticTVNativeObservationHarness(t, "qbittorrent")
		h.send(t)
		f.pool = h.fixture.pool
		op = h.automaticTVSenderHarness.request.OperationID
		key = bytes.Repeat([]byte{27}, 32)
	case "complete-tv":
		p, _, r := publisherCompleteTV(t)
		f.pool = p
		op = r.OperationID
		key = make([]byte, 32)
	case "movie-upgrade":
		h := nativeTrackingFixture(t, "Ok.")
		f.pool = h.p
		op = h.operation
		key = bytes.Repeat([]byte{37}, 32)
	case "tv-episode-upgrade":
		h := newTVUpgradeFixture(t)
		r := h.prepared(t)
		h.command(t, "send", r.OperationID, r.Version, "send")
		f.pool = h.pool
		op = r.OperationID
		key = bytes.Repeat([]byte{27}, 32)
	default:
		t.Fatal(scope)
	}
	if _, e := postgres.Migrate(t.Context(), f.pool.Config().ConnConfig.ConnString(), "../../../migrations"); e != nil {
		t.Fatal(e)
	}
	task, e := db.New(f.pool).GetDownloadTaskByOpID(t.Context(), op)
	if e != nil {
		t.Fatal(e)
	}
	f.task = task
	table, ownerColumn := "automatic_tv_execution_authority", "owner_id"
	if scope == "complete-tv" {
		table = "complete_tv_execution_authority"
		ownerColumn = "actor_id"
	}
	if scope == "movie-upgrade" {
		table = "movie_upgrade_authority"
		ownerColumn = "actor_id"
	}
	if scope == "tv-episode-upgrade" {
		table = "tv_episode_upgrade_authority"
		ownerColumn = "actor_id"
	}
	var owner uuid.UUID
	var role string
	if e = f.pool.QueryRow(t.Context(), "SELECT a."+ownerColumn+",u.role FROM "+table+" a JOIN app_user u ON u.id=a."+ownerColumn+" WHERE operation_id=$1", op).Scan(&owner, &role); e != nil {
		t.Fatal(e)
	}
	f.actor = &ports.Actor{Type: ports.ActorTypeUser, ID: owner.String(), Role: role}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "dedicated-control", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			_, _ = io.WriteString(w, "5.1.2")
		case "/api/v2/torrents/info":
			f.reads.Add(1)
			if hook := f.onRead.Load(); hook != nil {
				(*hook)()
			}
			if r.URL.Query().Get("hashes") != task.InfoHash {
				t.Error("native observation changed original hash")
			}
			tag := "pulsemedia:op:" + op.String()
			if f.badTag.Load() {
				tag = "pulsemedia:op:" + uuid.NewString()
			}
			state := "downloading"
			if f.paused.Load() {
				state = "stoppedDL"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": task.InfoHash, "save_path": task.SavePath, "tags": tag, "state": state, "progress": 0.5, "size": task.TotalBytes, "downloaded": task.TotalBytes / 2}})
		case "/api/v2/torrents/stop", "/api/v2/torrents/start":
			f.sends.Add(1)
			if r.Method != "POST" {
				t.Error("native mutation method changed")
			}
			if e := r.ParseForm(); e != nil || r.Form.Get("hashes") != task.InfoHash {
				t.Error("native mutation changed original hash")
			}
			f.paused.Store(strings.HasSuffix(r.URL.Path, "/stop"))
			if hook := f.onSend.Load(); hook != nil {
				(*hook)()
			}
			if f.loseACK.Swap(false) {
				conn, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
				} else {
					_ = conn.Close()
				}
				return
			}
		default:
			t.Error("unexpected control native request", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	config, e := db.New(f.pool).GetDownloadClientByID(t.Context(), task.ClientID)
	if e != nil {
		t.Fatal(e)
	}
	f.repo = download.NewRepository(f.pool, idgen.New(), key)
	f.repo.SetIndexerConfigurationDecoder(crypto.NewSecretProtector(key))
	f.repo.SetClientFactory(func(kind, address, user, password string) (ports.DownloadClient, error) {
		if kind != config.ClientType || address != config.Url || user != config.Username {
			t.Error("factory redirected original native endpoint")
		}
		return qbittorrent.NewClient(address, user, password, &http.Client{Transport: dedicatedControlTransport{target}, Timeout: 10 * time.Second})
	})
	f.service = downloadcase.NewControlService(f.repo)
	return f
}
func (f *dedicatedControlFixture) apply(ctx context.Context, key, action string) (*ports.DownloadControlReceipt, error) {
	return f.service.Apply(ctx, f.actor, f.task.OperationID, key, ports.DownloadControlCommand{Action: action, Confirmed: true})
}
func (f *dedicatedControlFixture) protected(t *testing.T) string {
	t.Helper()
	return publisherFrozen(t, f.pool, `SELECT jsonb_build_array((SELECT to_jsonb(t) FROM download_task t WHERE id=$2),(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM acquisition_reservation r WHERE operation_id=$1),(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM unit_reservation r WHERE operation_id=$1),(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM download_requirement r WHERE operation_id=$1),(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM job j WHERE payload->>'operation_id'=$1::text),(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM unit_fact f))::text`, f.task.OperationID, f.task.ID)
}

func TestDedicatedDownloadControlNativeHTTPPG(t *testing.T) {
	for _, scope := range dedicatedControlScopes {
		t.Run(scope, func(t *testing.T) {
			f := newDedicatedControl(t, scope)
			before := f.protected(t)
			got, e := f.apply(t.Context(), "pause", "pause")
			if e != nil || got == nil || got.State != "confirmed" {
				t.Fatal("pause", got, e)
			}
			got, e = f.apply(t.Context(), "resume", "resume")
			if e != nil || got.State != "confirmed" || f.paused.Load() {
				t.Fatal("resume", got, e)
			}
			f.loseACK.Store(true)
			got, e = f.apply(t.Context(), "lost-ack", "pause")
			if e != nil || got.State != "unknown" {
				t.Fatal("unknown", got, e)
			}
			if _, e = f.apply(t.Context(), "replacement", "resume"); !errors.Is(e, ports.ErrDownloadControlConflict) {
				t.Fatal("replacement send", e)
			}
			sends := f.sends.Load()
			got, e = f.apply(t.Context(), "lost-ack", "pause")
			if e != nil || got.State != "unknown" || f.sends.Load() != sends {
				t.Fatal("replay sent", got, e)
			}
			got, e = f.service.Get(t.Context(), f.actor, f.task.OperationID, "lost-ack")
			if e != nil || got.State != "unknown" {
				t.Fatal(got, e)
			}
			got, e = f.service.Observe(t.Context(), f.actor, f.task.OperationID, "lost-ack")
			if e != nil || got.State != "confirmed" || f.sends.Load() != 3 {
				t.Fatal("observation resent", got, e)
			}
			if f.protected(t) != before {
				t.Fatal("control changed acquisition/task/job/fact protections")
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_authority WHERE scope=$1`, scope); n != 3 {
				t.Fatal("missing immutable authority", n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_physical`); n != 0 {
				t.Fatal("physical return did not release own hold", n)
			}
		})
	}
}

func TestDedicatedDownloadControlCurrentAuthorityPG(t *testing.T) {
	for _, scope := range dedicatedControlScopes {
		t.Run(scope, func(t *testing.T) {
			for _, change := range []string{"owner", "intent", "endpoint", "tag", "metadata"} {
				t.Run(change, func(t *testing.T) {
					f := newDedicatedControl(t, scope)
					hook := func() {
						switch change {
						case "owner":
							execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
						case "intent":
							execIntent(t, f.pool, `UPDATE subscription SET intent_version=intent_version+1 WHERE id IN (SELECT subscription_id FROM automatic_tv_execution_authority WHERE operation_id=$1 UNION ALL SELECT subscription_id FROM complete_tv_execution_authority WHERE operation_id=$1 UNION ALL SELECT subscription_id FROM movie_upgrade_authority WHERE operation_id=$1)`, f.task.OperationID)
						case "endpoint":
							execIntent(t, f.pool, `UPDATE download_client SET url='https://changed.invalid' WHERE id=$1`, f.task.ClientID)
						case "metadata":
							execIntent(t, f.pool, `UPDATE media SET metadata_version=metadata_version+1 WHERE id IN (SELECT media_id FROM subscription WHERE id IN (SELECT subscription_id FROM automatic_tv_execution_authority WHERE operation_id=$1 UNION ALL SELECT subscription_id FROM complete_tv_execution_authority WHERE operation_id=$1 UNION ALL SELECT subscription_id FROM movie_upgrade_authority WHERE operation_id=$1))`, f.task.OperationID)
						}
					}
					if change == "tag" {
						f.badTag.Store(true)
					} else {
						f.onRead.Store(&hook)
					}
					if _, e := f.apply(t.Context(), "stale", "pause"); e == nil {
						t.Fatal("changed authority accepted")
					}
					if f.sends.Load() != 0 || intentCount(t, f.pool, `SELECT count(*) FROM download_control_command`) != 0 {
						t.Fatal("stale authority dispatched")
					}
				})
			}
		})
	}
}

func TestDedicatedDownloadControlPhysicalExclusivityPG(t *testing.T) {
	for _, scope := range dedicatedControlScopes {
		t.Run(scope, func(t *testing.T) {
			for _, lost := range []bool{false, true} {
				name := "cancel"
				if lost {
					name = "database_connection_loss"
				}
				t.Run(name, func(t *testing.T) {
					f := newDedicatedControl(t, scope)
					before := f.protected(t)
					entered, release := make(chan struct{}), make(chan struct{})
					hook := func() { close(entered); <-release }
					f.onSend.Store(&hook)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					done := make(chan error, 1)
					go func() { _, e := f.apply(ctx, "inflight", "pause"); done <- e }()
					var once sync.Once
					drain := func() {
						once.Do(func() {
							close(release)
							e := <-done
							if lost && e == nil {
								t.Error("lost session published success")
							}
							if !lost && e != nil {
								t.Error("physical return writeback", e)
							}
						})
					}
					defer drain()
					select {
					case <-entered:
					case <-time.After(10 * time.Second):
						t.Fatal("native send not entered")
					}
					cancel()
					if lost {
						execIntent(t, f.pool, `SELECT pg_terminate_backend(backend_pid) FROM download_control_command WHERE request_key='inflight'`)
					}
					if _, e := f.apply(t.Context(), "replacement", "resume"); !errors.Is(e, ports.ErrDownloadControlConflict) {
						t.Fatal("replacement admitted", e)
					}
					if _, e := f.service.Observe(t.Context(), f.actor, f.task.OperationID, "inflight"); !errors.Is(e, ports.ErrDownloadControlConflict) {
						t.Fatal("active call observed", e)
					}
					if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_physical`); n != 1 {
						t.Fatal("physical hold released early", n)
					}
					for _, sql := range []string{`UPDATE download_control_authority SET digest=repeat('f',64)`, `DELETE FROM download_control_physical`, `TRUNCATE download_control_physical`} {
						if _, e := f.pool.Exec(t.Context(), sql); e == nil {
							t.Fatal("physical/authority tamper accepted", sql)
						}
					}
					drain()
					want := 0
					if lost {
						want = 1
					}
					if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_physical`); n != want {
						t.Fatal("lost backend must retain hold", n, want)
					}
					if f.sends.Load() != 1 || f.protected(t) != before {
						t.Fatal("physical control changed acquisition protection")
					}
					if lost {
						if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_return`); n != 0 {
							t.Fatal("forged physical return")
						}
					}
				})
			}
		})
	}
}
