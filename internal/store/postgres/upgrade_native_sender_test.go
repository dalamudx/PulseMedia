package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/google/uuid"
)

type nativeUpgradeClock struct{}

func (nativeUpgradeClock) Now() time.Time                  { return time.Now() }
func (nativeUpgradeClock) Since(t time.Time) time.Duration { return time.Since(t) }

type nativeUpgradeFixture struct {
	*preparedUpgradeFixture
	manager            *download.Manager
	driver             ports.MovieUpgradeNativeSendDriver
	repo               ports.MovieUpgradeNativeSendRepository
	adds               atomic.Int32
	observationQueries atomic.Int32
	received           chan nativeUpgradeUpload
	hookMu             sync.Mutex
	hook               func() error
}
type nativeUpgradeUpload struct {
	raw        []byte
	path, tags string
}
type ignoringUpgradeClient struct {
	ports.DownloadClient
	sender ports.DownloadClient
}

func (c *ignoringUpgradeClient) MovieUpgradeByteSender() ports.DownloadClient {
	return &ignoringUpgradeAdd{c.sender}
}

type ignoringUpgradeAdd struct{ ports.DownloadClient }

func (c *ignoringUpgradeAdd) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	return c.DownloadClient.AddTorrent(context.WithoutCancel(ctx), p)
}

func nativeUpgrade(t *testing.T, ack string, ignoreCancel bool, beforeFreeze ...func(*frozenUpgradeFixture)) *nativeUpgradeFixture {
	t.Helper()
	n := &nativeUpgradeFixture{received: make(chan nativeUpgradeUpload, 4)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			if _, e := io.WriteString(w, "Ok."); e != nil {
				t.Error(e)
			}
			return
		}
		if r.URL.Path == "/api/v2/torrents/info" {
			n.observationQueries.Add(1)
			if _, e := io.WriteString(w, "[]"); e != nil {
				t.Error(e)
			}
			return
		}
		if r.URL.Path != "/api/v2/torrents/add" {
			http.NotFound(w, r)
			return
		}
		n.adds.Add(1)
		if e := r.ParseMultipartForm(ports.MaxTorrentMetainfoBytes); e != nil {
			t.Error(e)
			http.Error(w, "invalid form", 400)
			return
		}
		defer func() {
			if e := r.MultipartForm.RemoveAll(); e != nil {
				t.Error(e)
			}
		}()
		file, _, e := r.FormFile("torrents")
		if e != nil {
			t.Error(e)
			http.Error(w, "missing torrent", 400)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(file, ports.MaxTorrentMetainfoBytes+1))
		if closeErr := file.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		if e != nil {
			t.Error(e)
			http.Error(w, "read failed", 400)
			return
		}
		n.received <- nativeUpgradeUpload{raw: raw, path: r.FormValue("savepath"), tags: r.FormValue("tags")}
		n.hookMu.Lock()
		hook := n.hook
		n.hookMu.Unlock()
		if hook != nil {
			if e = hook(); e != nil {
				t.Error(e)
				http.Error(w, "hook failed", 500)
				return
			}
		}
		if _, e = io.WriteString(w, ack); e != nil {
			t.Error(e)
		}
	}))
	t.Cleanup(server.Close)
	key := bytes.Repeat([]byte{37}, 32)
	encrypted, e := crypto.Encrypt(key, []byte("native-test-password"))
	if e != nil {
		t.Fatal(e)
	}
	f := preparingUpgradeWithHook(t, func(f *frozenUpgradeFixture) {
		f.exec(t, "UPDATE download_client SET client_type='qbittorrent',url=$2,username='native-admin',password_encrypted=$3 WHERE id=$1", f.client, server.URL, encrypted)

		for _, hook := range beforeFreeze {
			hook(f)
		}
	})
	n.preparedUpgradeFixture = f
	if _, e = f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "native-prepare"); e != nil {
		t.Fatalf("prepare native upgrade fixture: %v", e)
	}
	if v, e := postgres.Migrate(context.Background(), f.dsn, dispatchMigrationDir(t)); e != nil || v != 68 {
		t.Fatal(v, e)
	}
	m := download.NewManager(f.p, idgen.New(), key)
	m.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
		c, e := qbittorrent.NewClient(url, user, password, nil)
		if e != nil {
			return nil, e
		}
		if ignoreCancel {
			return &ignoringUpgradeClient{DownloadClient: c, sender: c.MovieUpgradeByteSender()}, nil
		}
		return c, nil
	})
	issuer := postgres.NewMovieUpgradeNativeDispatchVerifier()
	driver, e := download.NewMovieUpgradeNativeSendDriver(m, issuer, nativeUpgradeClock{})
	if e != nil {
		t.Fatal(e)
	}
	repo, e := postgres.NewMovieUpgradeNativeSendRepository(f.preparation, driver, issuer)
	if e != nil {
		t.Fatal(e)
	}
	n.manager, n.driver, n.repo = m, driver, repo
	return n
}
func (n *nativeUpgradeFixture) setHook(f func() error) {
	n.hookMu.Lock()
	n.hook = f
	n.hookMu.Unlock()
}

func TestMovieUpgradeNativeSendPG(t *testing.T) {
	for _, c := range []struct{ ack, status string }{{"Ok.", "confirmed"}, {"ambiguous", "unknown"}} {
		t.Run(c.status, func(t *testing.T) {
			n := nativeUpgrade(t, c.ack, false)
			old, graph := n.oldState(t), nativeProtectedGraph(t, n.preparedUpgradeFixture)
			out, e := n.repo.SendMovieUpgrade(context.Background(), n.actor, n.operation, uuid.New(), n.proof, "native-send")
			if e != nil {
				t.Fatal(e)
			}
			if out.Status != c.status || out.ExecutionAllowed || out.OperationID != n.operation {
				t.Fatalf("bad native result: %+v", out)
			}
			got := <-n.received
			if !bytes.Equal(got.raw, n.proof.Bytes()) || got.path != "/never-open-frozen-upgrade" || got.tags != "pulsemedia,pulsemedia:op:"+n.operation.String() {
				t.Fatal("native upload changed frozen bytes/path/tags")
			}
			if n.adds.Load() != 1 || old != n.oldState(t) || graph != nativeProtectedGraph(t, n.preparedUpgradeFixture) {
				t.Fatal("native send changed protection or old facts")
			}
			if _, e = n.repo.SendMovieUpgrade(context.Background(), n.actor, n.operation, uuid.New(), n.proof, "native-send"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
				t.Fatalf("blind replay: %v", e)
			}
			if n.adds.Load() != 1 {
				t.Fatal("repeated Add")
			}
			var stored string
			if e = n.p.QueryRow(context.Background(), `SELECT to_jsonb(a)::text||to_jsonb(d)::text||to_jsonb(r)::text FROM movie_upgrade_send_authorization a JOIN movie_upgrade_send_dispatch d USING(operation_id) JOIN movie_upgrade_send_result r USING(operation_id) WHERE a.operation_id=$1`, n.operation).Scan(&stored); e != nil {
				t.Fatal(e)
			}
			if strings.Contains(stored, "private-passkey") || strings.Contains(stored, "native-test-password") {
				t.Fatal("cleartext secret persisted")
			}
		})
	}
}

func TestMovieUpgradeNativeConfigChangesDuringHTTPPG(t *testing.T) {
	n := nativeUpgrade(t, "Ok.", false)
	old := n.oldState(t)
	n.setHook(func() error {
		if _, e := n.p.Exec(context.Background(), "UPDATE library_root SET path=path||'-changed',generation=generation+1 WHERE id=$1", n.root); e != nil {
			return e
		}
		_, e := n.p.Exec(context.Background(), "UPDATE download_client SET url='http://127.0.0.1:1' WHERE id=$1", n.client)
		return e
	})
	out, e := n.repo.SendMovieUpgrade(context.Background(), n.actor, n.operation, uuid.New(), n.proof, "native-config")
	if e != nil || out == nil || out.Status != "confirmed" || old != n.oldState(t) || n.adds.Load() != 1 {
		t.Fatalf("original physical result lost: %+v %v", out, e)
	}
}

func TestMovieUpgradeNativePhysicalLifetimePG(t *testing.T) {
	for _, kill := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancellation", true: "connection_loss"}[kill], func(t *testing.T) {
			n := nativeUpgrade(t, "Ok.", true)
			var desc ports.MovieUpgradeNativeDescriptor
			desc.OperationID, desc.ClientID, desc.ClientDigest, desc.InfoHash = n.operation, n.client, frozenClientDigest(t, n.frozenUpgradeFixture), n.proof.InfoHash()
			sum := n.proof.SHA256()
			desc.RawSHA256 = fmtNativeSHA(sum)
			desc.SavePath = "/never-open-frozen-upgrade"
			if e := n.p.QueryRow(context.Background(), "SELECT id FROM download_task WHERE operation_id=$1", n.operation).Scan(&desc.TaskID); e != nil {
				t.Fatal(e)
			}
			other, e := n.driver.OpenMovieUpgradeNativeSend(context.Background(), desc)
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				if e := other.Close(context.Background()); e != nil {
					t.Error(e)
				}
			}()
			entered, release := make(chan struct{}), make(chan struct{})
			n.setHook(func() error {
				close(entered)
				select {
				case <-release:
					return nil
				case <-time.After(12 * time.Second):
					return errors.New("physical test owner failed to release")
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan struct{})
			var sendErr error
			var result *ports.MovieUpgradeSendResultReceipt
			go func() {
				defer close(finished)
				result, sendErr = n.repo.SendMovieUpgrade(ctx, n.actor, n.operation, uuid.New(), n.proof, "native-lifetime")
			}()
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer func() {
				unblock()
				select {
				case <-finished:
				case <-time.After(15 * time.Second):
					t.Error("native owner did not drain")
				}
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("Add not reached")
			}
			cancel()
			select {
			case <-finished:
				t.Fatal("logical cancel ended physical owner early")
			case <-time.After(100 * time.Millisecond):
			}
			if kill {
				var pid int
				if e = n.p.QueryRow(context.Background(), "SELECT backend_pid FROM movie_upgrade_send_authorization WHERE operation_id=$1", n.operation).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				if _, e = n.p.Exec(context.Background(), "SELECT pg_terminate_backend($1)", pid); e != nil {
					t.Fatal(e)
				}
			}
			retryCtx, done := context.WithTimeout(context.Background(), time.Second)
			_, e = n.repo.SendMovieUpgrade(retryCtx, n.actor, n.operation, uuid.New(), n.proof, "retry")
			done()
			if !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
				t.Fatalf("persistent authorization allowed replay: %v", e)
			}
			short, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_, _, e = n.manager.TestClient(short, n.client)
			stop()
			if !errors.Is(e, context.DeadlineExceeded) {
				t.Fatalf("budget released before physical return: %v", e)
			}
			var recorded int
			if e = n.p.QueryRow(context.Background(), "SELECT count(*) FROM movie_upgrade_send_result WHERE operation_id=$1", n.operation).Scan(&recorded); e != nil || recorded != 0 {
				t.Fatal("premature result", recorded, e)
			}
			unblock()
			select {
			case <-finished:
			case <-time.After(10 * time.Second):
				t.Fatal("physical owner did not finish")
			}
			if kill {
				if sendErr == nil {
					t.Fatal("lost original connection recorded a result")
				}
			} else if sendErr != nil || result == nil || result.Status != "confirmed" {
				t.Fatalf("physical return after cancellation lost: %+v %v", result, sendErr)
			}
			if n.adds.Load() != 1 {
				t.Fatal("physical replay")
			}
		})
	}
}
func fmtNativeSHA(sum [32]byte) string {
	return fmt.Sprintf("%x", sum)
}

func nativeProtectedGraph(t *testing.T, f *preparedUpgradeFixture) string {
	t.Helper()
	var graph string
	if e := f.p.QueryRow(context.Background(), `SELECT jsonb_build_object('p',to_jsonb(p),'t',to_jsonb(t),'r',(SELECT jsonb_agg(to_jsonb(x)) FROM unit_reservation x WHERE operation_id=p.operation_id),'d',(SELECT jsonb_agg(to_jsonb(x)) FROM download_requirement x WHERE operation_id=p.operation_id),'l',(SELECT jsonb_agg(to_jsonb(x)) FROM acquisition_reservation x WHERE operation_id=p.operation_id))::text FROM movie_upgrade_preparation p JOIN download_task t ON t.id=p.task_id WHERE p.operation_id=$1`, f.operation).Scan(&graph); e != nil {
		t.Fatal(e)
	}
	return graph
}
