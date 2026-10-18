package httpapi_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type byteSenderFixture struct {
	*prepareTVFixture
	manager                *download.Manager
	fetches, adds, invalid atomic.Int32
	source, downloader     *httptest.Server
	key                    []byte
	expectedPath           string
	raw                    string
	fetchHook              func()
	addStatus              int
	addBody                string
}

func newByteSenderFixture(t *testing.T, raw string) *byteSenderFixture {
	t.Helper()
	s := &byteSenderFixture{key: bytes.Repeat([]byte{23}, 32), raw: raw, addStatus: 200, addBody: "Ok."}
	s.source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.fetches.Add(1)
		if r.URL.RawQuery != "passkey=private-link" {
			s.invalid.Add(1)
		}
		if s.fetchHook != nil {
			s.fetchHook()
		}
		if s.raw == "redirect" {
			http.Redirect(w, r, s.downloader.URL+"/secret", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, s.raw)
	}))
	t.Cleanup(s.source.Close)
	s.downloader = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_ = r.ParseForm()
			if r.Form.Get("username") != "current-user" || r.Form.Get("password") != "private-password" {
				s.invalid.Add(1)
			}
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/add":
			s.adds.Add(1)
			if e := r.ParseMultipartForm(1 << 20); e != nil {
				s.invalid.Add(1)
				w.WriteHeader(500)
				return
			}
			defer func() { _ = r.MultipartForm.RemoveAll() }()
			f, _, e := r.FormFile("torrents")
			if e != nil {
				s.invalid.Add(1)
				w.WriteHeader(500)
				return
			}
			defer func() { _ = f.Close() }()
			b, _ := io.ReadAll(f)
			if !bytes.Equal(b, []byte(completeTVRaw)) || r.FormValue("urls") != "" || r.FormValue("savepath") != s.expectedPath || r.FormValue("tags") != "pulsemedia,pulsemedia:op:"+s.c.OperationID.String() {
				s.invalid.Add(1)
			}
			w.WriteHeader(s.addStatus)
			_, _ = io.WriteString(w, s.addBody)
		default:
			s.invalid.Add(1)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(s.downloader.Close)
	s.prepareTVFixture = newPrepareTVFixture(t, func(f *completeTVFixture) {
		enc := func(v string) []byte {
			b, e := crypto.Encrypt(s.key, []byte(v))
			if e != nil {
				t.Fatal(e)
			}
			return b
		}
		f.exec(t, `UPDATE download_client SET url=$1,username='current-user',password_encrypted=$2`, s.downloader.URL, enc("private-password"))
		f.exec(t, `UPDATE indexer SET url=$1,api_key_encrypted=$2`, s.source.URL, enc("private-api-key"))
		f.exec(t, `UPDATE release_candidate SET link_encrypted=$1,source_config_version=(SELECT config_version FROM indexer WHERE id=indexer_id)`, enc(s.source.URL+"/native?passkey=private-link"))
		f.exec(t, `UPDATE indexer_query SET config_version=(SELECT config_version FROM indexer WHERE id=indexer_id)`)
	})
	if e := s.f.a.DBPool().QueryRow(context.Background(), `SELECT path FROM library_root WHERE id=(SELECT root_id FROM subscription LIMIT 1)`).Scan(&s.expectedPath); e != nil {
		t.Fatal(e)
	}
	s.manager = s.newManager()
	return s
}
func (s *byteSenderFixture) configureCompleteTV(m *download.Manager) {
	repo := postgres.NewCompleteTVSubmissionStore(s.f.a.DBPool(), idgen.New())
	m.SetCompleteTVServices(repo, repo, torznab.CompleteTVFetcher{}, torrent.Inspector{})
}
func (s *byteSenderFixture) newManager() *download.Manager {
	m := download.NewManager(s.f.a.DBPool(), idgen.New(), s.key)
	m.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
		return qbittorrent.NewClient(url, user, password, nil)
	})
	s.configureCompleteTV(m)
	return m
}
func (s *byteSenderFixture) send(ctx context.Context) (*ports.CompleteTVSubmissionResultReceipt, error) {
	return s.manager.SendCompleteTV(ctx, s.c.Lease, s.c.OperationID)
}
func (s *byteSenderFixture) state(t *testing.T) string {
	t.Helper()
	var v string
	if e := s.f.a.DBPool().QueryRow(context.Background(), `SELECT state FROM download_task WHERE operation_id=$1`, s.c.OperationID).Scan(&v); e != nil {
		t.Fatal(e)
	}
	return v
}
func (s *byteSenderFixture) protected(t *testing.T) {
	t.Helper()
	var lanes, reservations, demands int
	if e := s.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL),(SELECT count(*) FROM unit_reservation WHERE state='dispatched'),(SELECT count(*) FROM download_requirement WHERE state='pending')`).Scan(&lanes, &reservations, &demands); e != nil {
		t.Fatal(e)
	}
	if lanes != 2 || reservations != 2 || demands != 2 {
		t.Fatal("lost protection", lanes, reservations, demands)
	}
}
func TestCompleteTVByteSenderNativePGExactAndNoResend(t *testing.T) {
	s := newByteSenderFixture(t, completeTVRaw)
	// A cached instance is deliberately unusable. The sender must construct current DB config.
	stale, e := qbittorrent.NewClient("http://127.0.0.1:1", "old-user", "old-password", nil)
	if e != nil {
		t.Fatal(e)
	}
	s.manager.RegisterClient(sClientID(t, s), stale)
	r, e := s.send(context.Background())
	if e != nil || r == nil || r.State != "tracking" {
		t.Fatal(r, e)
	}
	if s.fetches.Load() != 1 || s.adds.Load() != 1 || s.invalid.Load() != 0 || s.state(t) != "tracking" {
		t.Fatal("native wire mismatch", s.fetches.Load(), s.adds.Load(), s.invalid.Load())
	}
	var hash, path, kind, job string
	var units int
	if e = s.f.a.DBPool().QueryRow(context.Background(), `SELECT t.info_hash,t.save_path,p.proof->>'kind',j.state,(SELECT count(*) FROM complete_tv_submission_unit) FROM download_task t JOIN complete_tv_submission_preparation p ON p.task_id=t.id JOIN job j ON j.id=p.job_id`).Scan(&hash, &path, &kind, &job, &units); e != nil {
		t.Fatal(e)
	}
	if hash != "d497dcf93e2dbd4b917ba68788ba7d42590cf594" || path != s.expectedPath || kind != "complete_tv_prepared_v2" || job != "succeeded" || units != 2 {
		t.Fatal("bad persisted result")
	}
	for range 2 {
		r, e = s.send(context.Background())
		if r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
			t.Fatal(r, e)
		}
	}
	if s.fetches.Load() != 1 || s.adds.Load() != 1 {
		t.Fatal("replayed physical action")
	}
	s.protected(t)
}
func sClientID(t *testing.T, s *byteSenderFixture) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if e := s.f.a.DBPool().QueryRow(context.Background(), `SELECT client_id FROM complete_tv_execution_authority`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}
func TestCompleteTVByteSenderNativePGNoAddFences(t *testing.T) {
	cases := map[string]struct {
		raw, sql string
		during   bool
	}{
		"client-before":   {completeTVRaw, `UPDATE download_client SET username='changed'`, false},
		"client-during":   {completeTVRaw, `UPDATE download_client SET username='changed'`, true},
		"source-during":   {completeTVRaw, `UPDATE indexer SET url='http://changed.invalid',config_version=config_version+1`, true},
		"coverage-during": {completeTVRaw, `UPDATE media_unit SET metadata_version=metadata_version+1`, true},
		"stale-source":    {completeTVRaw, `UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes'`, false},
		"expired-lease":   {completeTVRaw, `UPDATE job SET lease_until=clock_timestamp() WHERE kind='complete_tv_coverage_download_submission'`, false},
		"expanded":        {strings.Replace(completeTVRaw, "Show.S01E02.mkv", "Show.S01E03.mkv", 1), "", false},
		"invalid-native":  {"not bencode", "", false},
		"redirect":        {"redirect", "", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newByteSenderFixture(t, c.raw)
			if c.sql != "" {
				if c.during {
					s.fetchHook = func() { s.exec(t, c.sql) }
				} else {
					s.exec(t, c.sql)
				}
			}
			before := s.preparedCounts(t)
			r, e := s.send(context.Background())
			if r != nil || e == nil {
				t.Fatal(r, e)
			}
			if s.adds.Load() != 0 || s.preparedCounts(t) != before {
				t.Fatal("stale evidence sent or wrote task")
			}
			if strings.Contains(e.Error(), "private-") || strings.Contains(e.Error(), s.source.URL) {
				t.Fatal("secret in error")
			}
		})
	}
}
func TestCompleteTVByteSenderNativePGExistingProofAndUnknown(t *testing.T) {
	for _, state := range []string{"submitting", "unknown", "tracking"} {
		t.Run(state, func(t *testing.T) {
			s := newByteSenderFixture(t, completeTVRaw)
			if _, e := s.prepare(); e != nil {
				t.Fatal(e)
			}
			if state != "submitting" {
				s.exec(t, `UPDATE download_task SET state=$1`, state)
			}
			r, e := s.send(context.Background())
			if r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
				t.Fatal(r, e)
			}
			if s.fetches.Load() != 0 || s.adds.Load() != 0 {
				t.Fatal("existing task fetched or resent")
			}
			s.protected(t)
		})
	}
}
func TestCompleteTVByteSenderNativePGErrorUnknownAndUnavailable(t *testing.T) {
	t.Run("remote-error", func(t *testing.T) {
		s := newByteSenderFixture(t, completeTVRaw)
		s.addStatus = 500
		r, e := s.send(context.Background())
		if e != nil || r == nil || r.State != "unknown" || s.state(t) != "unknown" {
			t.Fatal(r, e)
		}
		s.protected(t)
		_, e = s.send(context.Background())
		if !errors.Is(e, ports.ErrCompleteTVNeedsObservation) || s.adds.Load() != 1 || s.fetches.Load() != 1 {
			t.Fatal("unknown replay", e)
		}
		var bad int
		if e = s.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE payload::text LIKE '%private-%'`).Scan(&bad); e != nil || bad != 0 {
			t.Fatal("sensitive audit", bad, e)
		}
	})
	t.Run("late-db-error", func(t *testing.T) {
		s := newByteSenderFixture(t, completeTVRaw)
		s.exec(t, `CREATE FUNCTION fail_sender_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='tracking' THEN RAISE EXCEPTION 'private-database-credential'; END IF; RETURN NEW; END $$`)
		s.exec(t, `CREATE TRIGGER fail_sender_result BEFORE UPDATE ON download_task FOR EACH ROW EXECUTE FUNCTION fail_sender_result()`)
		r, e := s.send(context.Background())
		if r != nil || !errors.Is(e, ports.ErrUnavailable) || strings.Contains(e.Error(), "private-") {
			t.Fatal(r, e)
		}
		if s.state(t) != "submitting" {
			t.Fatal("partial result")
		}
		s.protected(t)
		_, e = s.send(context.Background())
		if !errors.Is(e, ports.ErrCompleteTVNeedsObservation) || s.adds.Load() != 1 {
			t.Fatal("DB error replay", e)
		}
	})
}

type ignoringCompleteTVClient struct {
	ports.DownloadClient
	entered  chan struct{}
	release  chan struct{}
	observed *ports.AddTorrentParam
}

func (c *ignoringCompleteTVClient) CompleteTVByteSender() ports.DownloadClient { return c }
func (c *ignoringCompleteTVClient) AddTorrent(_ context.Context, p ports.AddTorrentParam) error {
	*c.observed = p
	close(c.entered)
	<-c.release
	return errors.New("private-ignored-cancel")
}
func TestCompleteTVByteSenderNativePGPhysicalLifetime(t *testing.T) {
	for _, mode := range []string{"canceled", "expired", "lost-session"} {
		t.Run(mode, func(t *testing.T) {
			s := newByteSenderFixture(t, completeTVRaw)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var observed ports.AddTorrentParam
			s.manager.SetClientFactory(func(_, _, _, _ string) (ports.DownloadClient, error) {
				return &ignoringCompleteTVClient{entered: entered, release: release, observed: &observed}, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				r *ports.CompleteTVSubmissionResultReceipt
				e error
			}
			done := make(chan result, 1)
			go func() { r, e := s.send(ctx); done <- result{r, e} }()
			defer func() {
				unblock()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					t.Error("sender did not drain")
				}
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("no physical Add")
			}
			if !bytes.Equal(observed.TorrentBytes, []byte(completeTVRaw)) || observed.DownloadURL != "" || observed.InfoHash != "d497dcf93e2dbd4b917ba68788ba7d42590cf594" || observed.SavePath != s.expectedPath {
				t.Fatal("wrong Add contract")
			}
			if mode == "canceled" {
				cancel()
			}
			if mode == "expired" {
				s.exec(t, `UPDATE job SET lease_until=clock_timestamp() WHERE id=$1`, s.c.Lease.ID)
			}
			var locks int
			if e := s.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND pid<>pg_backend_pid()`).Scan(&locks); e != nil || locks < 1 {
				t.Fatal("physical lock not retained", locks, e)
			}
			if mode == "lost-session" {
				// Simulate death of only the dedicated physical session. Persisted task/proof
				// must prevent Add even though the advisory lock disappears.
				s.exec(t, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND pid<>pg_backend_pid()`)
			}
			other := s.newManager()
			r, e := other.SendCompleteTV(context.Background(), s.c.Lease, s.c.OperationID)
			if r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) || s.fetches.Load() != 1 {
				t.Fatal("overlap admitted", r, e, s.fetches.Load())
			}
			select {
			case <-done:
				t.Fatal("released before physical return")
			default:
			}
			s.protected(t)
			unblock()
			select {
			case got := <-done:
				// Reinsert the terminal receipt for the cleanup join.
				done <- got
				if mode == "expired" {
					if !errors.Is(got.e, ports.ErrCompleteTVResultStale) || s.state(t) != "submitting" {
						t.Fatal(got.e)
					}
				} else {
					if got.e != nil || got.r == nil || got.r.State != "unknown" {
						t.Fatal(got.r, got.e)
					}
				}
			case <-time.After(10 * time.Second):
				t.Fatal("physical return not joined")
			}
			s.protected(t)
			_, e = other.SendCompleteTV(context.Background(), s.c.Lease, s.c.OperationID)
			if !errors.Is(e, ports.ErrCompleteTVNeedsObservation) || s.fetches.Load() != 1 {
				t.Fatal("late retry fetched", e)
			}
		})
	}
}
