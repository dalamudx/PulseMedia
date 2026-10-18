package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type upgradeObservationTransport struct {
	endpoint *url.URL
	ignore   bool
}

func (t upgradeObservationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/api/v2/torrents/info" {
		return http.DefaultTransport.RoundTrip(r)
	}
	ctx := r.Context()
	if t.ignore {
		ctx = context.WithoutCancel(ctx)
	}
	copy := r.Clone(ctx)
	u := *r.URL
	u.Scheme = t.endpoint.Scheme
	u.Host = t.endpoint.Host
	copy.URL = &u
	return http.DefaultTransport.RoundTrip(copy)
}

type nativeObservationFixture struct {
	n                      *nativeUpgradeFixture
	repo                   ports.MovieUpgradeNativeObservationRepository
	queries                atomic.Int32
	body                   string
	status                 int
	blocking               bool
	entered, release       chan struct{}
	enterOnce, releaseOnce sync.Once
}

func newNativeObservation(t *testing.T, n *nativeUpgradeFixture, blocking bool) *nativeObservationFixture {
	t.Helper()
	f := &nativeObservationFixture{n: n, status: http.StatusOK, blocking: blocking, entered: make(chan struct{}), release: make(chan struct{})}
	var size int64
	if e := n.p.QueryRow(context.Background(), `SELECT (evidence->'proof'->>'total_bytes')::bigint FROM movie_upgrade_preparation WHERE operation_id=$1`, n.operation).Scan(&size); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal([]map[string]any{{"hash": n.proof.InfoHash(), "save_path": "/never-open-frozen-upgrade", "size": size, "downloaded": int64(0), "progress": 0, "state": "downloading", "tags": "pulsemedia:op:" + n.operation.String()}})
	if e != nil {
		t.Fatal(e)
	}
	f.body = string(raw)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.queries.Add(1)
		if r.URL.Path != "/api/v2/torrents/info" || r.URL.Query().Get("tag") != "pulsemedia:op:"+n.operation.String() {
			t.Error("wrong native read filter")
			http.Error(w, "invalid", 400)
			return
		}
		if f.blocking {
			f.enterOnce.Do(func() { close(f.entered) })
			select {
			case <-f.release:
			case <-time.After(12 * time.Second):
				http.Error(w, "owner did not drain", 400)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		if _, err := io.WriteString(w, f.body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(f.unblock)
	endpoint, e := url.Parse(server.URL)
	if e != nil {
		t.Fatal(e)
	}
	n.manager.SetClientFactory(func(_, base, user, password string) (ports.DownloadClient, error) {
		return qbittorrent.NewClient(base, user, password, &http.Client{Transport: upgradeObservationTransport{endpoint, blocking}})
	})
	issuer := postgres.NewMovieUpgradeNativeQueryVerifier()
	driver, e := download.NewMovieUpgradeNativeObservationDriver(n.manager, issuer, nativeUpgradeClock{})
	if e != nil {
		t.Fatal(e)
	}
	f.repo, e = postgres.NewMovieUpgradeNativeObservationRepository(n.preparation, driver, issuer)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *nativeObservationFixture) unblock() { f.releaseOnce.Do(func() { close(f.release) }) }
func TestMovieUpgradeNativeObservationPG(t *testing.T) {
	for _, kind := range []string{"exact_match", "absent", "multiple", "mismatch", "case_alias", "unicode_alias", "null", "http_error"} {
		t.Run(kind, func(t *testing.T) {
			n := r3Native(t, "uncertain", true)
			f := newNativeObservation(t, n, false)
			before := r3Protected(t, n)
			want := "unknown"
			switch kind {
			case "exact_match":
				want = "exact_match"
			case "absent":
				f.body = "[]"
				want = "absent"
			case "multiple":
				f.body = "[" + strings.Trim(f.body, "[]") + "," + strings.Trim(f.body, "[]") + "]"
				want = "ambiguous"
			case "mismatch":
				f.body = strings.Replace(f.body, n.proof.InfoHash(), strings.Repeat("f", 40), 1)
				want = "ambiguous"
			case "case_alias":
				f.body = strings.Replace(f.body, "\"save_path\":", "\"SAVE_PATH\":\"/wrong\",\"save_path\":", 1)
			case "unicode_alias":
				f.body = strings.Replace(f.body, "\"size\":", "\"ſize\":1,\"size\":", 1)
			case "null":
				f.body = "null"
			case "http_error":
				f.status = http.StatusBadRequest
			}
			out, e := f.repo.ObserveMovieUpgrade(context.Background(), n.actor, n.operation, uuid.New(), "native-observe", "native-observed")
			if e != nil || out == nil {
				t.Fatal("native observation failed", e)
			}
			if out.Outcome != want || out.Disposition != "manual_review" || out.ExecutionAllowed {
				t.Fatalf("outcome %s, expected %s", out.Outcome, want)
			}
			if f.queries.Load() != 1 || n.adds.Load() != 1 || r3Protected(t, n) != before || r3Count(t, n, "movie_upgrade_observation_permit") != 1 || r3Count(t, n, "movie_upgrade_observation_result") != 1 {
				t.Fatal("query repeated or protected state changed")
			}
			if _, e = f.repo.ObserveMovieUpgrade(context.Background(), n.actor, n.operation, uuid.New(), "native-observe", "native-observed"); !errors.Is(e, ports.ErrMovieUpgradeObservationReview) {
				t.Fatal("historical permit replayed", e)
			}
			if f.queries.Load() != 1 {
				t.Fatal("recovery executed query")
			}
		})
	}
}
func TestMovieUpgradeNativeObservationAdmissionPG(t *testing.T) {
	for _, ack := range []string{"missing", "Ok."} {
		t.Run(ack, func(t *testing.T) {
			n := r3Native(t, ack, ack != "missing")
			f := newNativeObservation(t, n, false)
			before := r3Protected(t, n)
			if _, e := f.repo.ObserveMovieUpgrade(context.Background(), n.actor, n.operation, uuid.New(), "no-admit", "no-result"); !errors.Is(e, ports.ErrMovieUpgradeObservationReview) {
				t.Fatal("missing/confirmed return admitted", e)
			}
			if f.queries.Load() != 0 || r3Count(t, n, "movie_upgrade_observation_permit") != 0 || r3Protected(t, n) != before {
				t.Fatal("rejected command queried or mutated")
			}
		})
	}
}
func TestMovieUpgradeNativeObservationPhysicalLifetimePG(t *testing.T) {
	for _, kind := range []string{"cancellation", "connection_loss", "configuration_change"} {
		t.Run(kind, func(t *testing.T) {
			n := r3Native(t, "uncertain", true)
			f := newNativeObservation(t, n, true)
			var desc ports.MovieUpgradeNativeDescriptor
			desc.OperationID, desc.ClientID, desc.ClientDigest, desc.InfoHash = n.operation, n.client, frozenClientDigest(t, n.frozenUpgradeFixture), n.proof.InfoHash()
			desc.RawSHA256 = fmtNativeSHA(n.proof.SHA256())
			desc.SavePath = "/never-open-frozen-upgrade"
			if e := n.p.QueryRow(context.Background(), "SELECT id FROM download_task WHERE operation_id=$1", n.operation).Scan(&desc.TaskID); e != nil {
				t.Fatal(e)
			}
			other, e := n.driver.OpenMovieUpgradeNativeSend(context.Background(), desc)
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				if err := other.Close(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan struct{})
			var result *ports.MovieUpgradeObservationResult
			var observeErr error
			go func() {
				defer close(finished)
				result, observeErr = f.repo.ObserveMovieUpgrade(ctx, n.actor, n.operation, uuid.New(), "lifetime-admit", "lifetime-result")
			}()
			defer func() {
				f.unblock()
				select {
				case <-finished:
				case <-time.After(15 * time.Second):
					t.Error("physical owner failed to drain")
				}
			}()
			select {
			case <-f.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("native query not reached")
			}
			cancel()
			select {
			case <-finished:
				t.Fatal("logical cancellation released physical owner")
			case <-time.After(100 * time.Millisecond):
			}
			if kind == "connection_loss" {
				var pid int
				if e = n.p.QueryRow(context.Background(), "SELECT backend_pid FROM movie_upgrade_observation WHERE operation_id=$1", n.operation).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				if _, e = n.p.Exec(context.Background(), "SELECT pg_terminate_backend($1)", pid); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "configuration_change" {
				if _, e = n.p.Exec(context.Background(), "UPDATE download_client SET url='http://127.0.0.1:1' WHERE id=$1", n.client); e != nil {
					t.Fatal(e)
				}
			}
			short, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_, _, e = n.manager.TestClient(short, n.client)
			stop()
			if !errors.Is(e, context.DeadlineExceeded) {
				t.Fatal("query released budget before physical return", e)
			}
			if kind != "configuration_change" {
				retry, done := context.WithTimeout(context.Background(), time.Second)
				_, e = f.repo.ObserveMovieUpgrade(retry, n.actor, n.operation, uuid.New(), "retry", "retry-result")
				done()
				if !errors.Is(e, ports.ErrMovieUpgradeObservationReview) {
					t.Fatal("overlapping query allowed", e)
				}
			}
			if r3Count(t, n, "movie_upgrade_observation_result") != 0 {
				t.Fatal("result recorded before physical return")
			}
			f.unblock()
			select {
			case <-finished:
			case <-time.After(10 * time.Second):
				t.Fatal("owner did not finish")
			}
			if kind == "cancellation" {
				if observeErr != nil || result == nil || result.Outcome != "exact_match" {
					t.Fatal("returned query lost after cancellation", observeErr)
				}
			} else if observeErr == nil {
				t.Fatal("stale original connection/config committed")
			}
			if f.queries.Load() != 1 || n.adds.Load() != 1 || r3Count(t, n, "movie_upgrade_observation_permit") != 1 {
				t.Fatal("physical permit/query/Add repeated")
			}
		})
	}
}
func TestMovieUpgradeNativeObservationRejectsTokenAndForgedQueryPG(t *testing.T) {
	n := r3Native(t, "uncertain", true)
	f := newNativeObservation(t, n, false)
	actor := *n.actor
	actor.Scopes = []string{"read"}
	if _, e := f.repo.ObserveMovieUpgrade(context.Background(), &actor, n.operation, uuid.New(), "token", "token-result"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("token admitted", e)
	}
	if _, e := postgres.NewMovieUpgradeNativeQueryVerifier().ConsumeMovieUpgradeQuery(context.Background(), struct{ Returned bool }{true}, ports.MovieUpgradeNativeObservationDescriptor{}); e == nil {
		t.Fatal("forged query accepted")
	}
	if f.queries.Load() != 0 || r3Count(t, n, "movie_upgrade_observation_permit") != 0 {
		t.Fatal("invalid caller queried")
	}
}
