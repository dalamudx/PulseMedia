package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type m11PersonNative struct {
	f           *m5IndexerAcceptance
	primary     *m5CreationProtocol
	personCalls atomic.Int32
	allCalls    atomic.Int32
	block       atomic.Bool
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
	enteredOnce sync.Once
}

func newM11PersonNative(t *testing.T) *m11PersonNative {
	t.Helper()
	f, p := newM5CreationAcceptance(t)
	h := &m11PersonNative{f: f, primary: p, entered: make(chan struct{}), release: make(chan struct{})}
	native := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.allCalls.Add(1)
		if r.URL.Path == "/3/person/525/combined_credits" {
			h.personCalls.Add(1)
			if r.Method != http.MethodGet || r.URL.Query().Get("api_key") == "" || r.URL.Query().Get("language") != "zh-CN" || r.Header.Get("Authorization") != "" {
				t.Error("native person identity or auth mismatch")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if h.block.Load() {
				h.enteredOnce.Do(func() { close(h.entered) })
				<-h.release
			}
			movie := map[string]any{"id": 550, "media_type": "movie", "title": "Native trusted movie", "original_title": "Native trusted movie", "release_date": "1999-10-15", "job": "Director", "department": "Directing"}
			tv := map[string]any{"id": 550, "media_type": "tv", "name": "Native sparse television", "original_name": "Native sparse television", "first_air_date": "2020-01-01", "job": "Director", "department": "Directing"}
			unknown := map[string]any{"id": 551, "media_type": "movie", "title": "Unknown release date", "release_date": "", "job": "Director", "department": "Directing"}
			old := map[string]any{"id": 552, "media_type": "movie", "title": "Before selected date", "release_date": "1900-01-01", "job": "Director", "department": "Directing"}
			nonDirector := map[string]any{"id": 553, "media_type": "movie", "title": "A writer credit", "release_date": "2025-01-01", "job": "Writer", "department": "Writing"}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{"id": 525, "cast": []any{movie, tv, unknown, old}, "crew": []any{movie, tv, unknown, old, nonDirector}}); err != nil {
				t.Error("owned native response failed")
			}
			return
		}
		p.serve(t, w, r)
	}))
	t.Cleanup(native.Close)
	t.Cleanup(func() { h.once.Do(func() { close(h.release) }) })
	target, err := url.Parse(native.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := native.Client().Transport
	p.mu.Lock()
	p.tvdbTransport = m11TVArtworkTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.themoviedb.org" || r.URL.User != nil {
			return nil, errors.New("unexpected owned person metadata destination")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		clone.URL, clone.Host = &u, target.Host
		return transport.RoundTrip(clone)
	})
	p.mu.Unlock()
	m9WordHTTPStable(t, f)
	return h
}
func (h *m11PersonNative) save(t *testing.T, role string) (uuid.UUID, ports.PersonFollowInput, string) {
	t.Helper()
	input := personFollowAcceptanceInput(t, h.f)
	input.Settings.Role = role
	input.Settings.MinReleaseDate = "1990-01-01"
	var tv uuid.UUID
	if err := h.f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM library_root WHERE media_type='tv' AND health='online' AND baseline_completed ORDER BY id LIMIT 1`).Scan(&tv); err != nil {
		t.Fatal(err)
	}
	input.Settings.TVRootID = &tv
	id := uuid.New()
	key := "native-person-" + role
	receipt := personFollowAcceptanceReceipt(t, h.f.request(t, http.MethodPut, "/api/v1/person-follows/"+id.String(), key, input, h.f.session))
	if receipt.Config.Id != id || receipt.Config.Version != 1 {
		t.Fatal("native follow configuration not persisted")
	}
	return id, input, key
}
func waitM11Person(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(35 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal(description)
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func TestM11PersonFollowNativeMovieTVPG(t *testing.T) {
	for _, role := range []string{"actor", "director"} {
		t.Run(role, func(t *testing.T) {
			h := newM11PersonNative(t)
			f := h.f
			id, _, key := h.save(t, role)
			waitM11Person(t, "production person sync did not complete", func() bool {
				var complete bool
				err := f.app.DBPool().QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM person_follow_sync_run WHERE config_id=$1 AND status='partial')`, id).Scan(&complete)
				return err == nil && complete
			})
			var exact bool
			if err := f.app.DBPool().QueryRow(t.Context(), `SELECT count(*)=2 AND count(DISTINCT sub.id)=2 AND count(DISTINCT i.media_type)=2 AND bool_and(sub.user_id=r.owner_id AND mi.namespace='tmdb.'||i.media_type AND mi.external_id='550' AND sub.root_id=i.root_id AND sub.state='active' AND sub.scope_type='all') FROM person_follow_sync_item i JOIN person_follow_sync_run r ON r.id=i.run_id JOIN subscription sub ON sub.id=i.subscription_id JOIN media_identity mi ON mi.media_id=sub.media_id WHERE r.config_id=$1 AND i.state='created' AND mi.namespace LIKE 'tmdb.%'`, id).Scan(&exact); err != nil || !exact {
				t.Fatal("person role did not create distinct real movie/TV identities", err)
			}
			var wanted []string
			rows, err := f.app.DBPool().Query(t.Context(), `SELECT u.unit_key FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id ORDER BY u.unit_key`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var k string
				if err = rows.Scan(&k); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				wanted = append(wanted, k)
			}
			err = rows.Err()
			rows.Close()
			if err != nil || !reflect.DeepEqual(wanted, []string{"movie", "S01E01", "S01E03", "S02E01", "S02E03"}) {
				t.Fatal("sparse Wanted differs from actual native episodes", wanted, err)
			}
			var unknown, old int
			if err = f.app.DBPool().QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE state='review_required' AND tmdb_id=551 AND source->>'ReleaseDate'=''),count(*) FILTER(WHERE state='excluded' AND tmdb_id=552) FROM person_follow_sync_item`).Scan(&unknown, &old); err != nil || unknown != 1 || old != 1 {
				t.Fatal("unknown date or old work auto-created", unknown, old, err)
			}
			var downloads int
			if err = f.app.DBPool().QueryRow(t.Context(), `SELECT count(*) FROM download_task`).Scan(&downloads); err != nil || downloads != 0 {
				t.Fatal("unbound follows started downloads", downloads, err)
			}
			if h.personCalls.Load() != 1 {
				t.Fatal("one bounded sync repeated person source", h.personCalls.Load())
			}
			original := personFollowAcceptanceReceipt(t, f.request(t, http.MethodGet, "/api/v1/person-follows/receipts/"+key, "", nil, f.session))
			pool, err := pgxpool.New(t.Context(), f.app.DBPool().Config().ConnString())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			f.stopRuntime()
			calls := h.allCalls.Load()
			var results int
			if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM person_follow_sync_item WHERE state='created'`).Scan(&results); err != nil || results != 2 {
				t.Fatal("App shutdown lost committed results", err)
			}
			if original.Config.Id != id || original.Config.Settings.Role != httpapi.PersonFollowSettingsRole(role) || h.allCalls.Load() != calls {
				t.Fatal("historical config or shutdown changed results")
			}
			t.Logf("M11_PERSON_NATIVE_APP_PASS role=%s App=PRODUCTION Cookie=REAL metadata=TLS_NATIVE credits=ONCE identity=MOVIE_TV_DISTINCT Wanted=SPARSE unknown_date=REVIEW source=SEALED downloads=ZERO", role)
		})
	}
}
func TestM11PersonFollowLateDisablePG(t *testing.T) {
	h := newM11PersonNative(t)
	f := h.f
	h.block.Store(true)
	id, input, _ := h.save(t, "director")
	select {
	case <-h.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("native person read did not start")
	}
	input.ExpectedVersion = 1
	input.Settings.Enabled = false
	personFollowAcceptanceReceipt(t, f.request(t, http.MethodPut, "/api/v1/person-follows/"+id.String(), "disable-during-read", input, f.session))
	h.once.Do(func() { close(h.release) })
	waitM11Person(t, "disabled person run did not reach aborted recovery", func() bool {
		var aborted bool
		err := f.app.DBPool().QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM person_follow_sync_run WHERE config_id=$1 AND status='aborted')`, id).Scan(&aborted)
		return err == nil && aborted
	})
	var protected bool
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT (SELECT count(*) FROM person_follow_sync_source)=0 AND (SELECT count(*) FROM person_follow_sync_item)=0 AND (SELECT count(*) FROM subscription)=0 AND (SELECT count(*) FROM wanted_item)=0 AND (SELECT count(*) FROM download_task)=0 AND (SELECT count(*) FROM person_follow_execution WHERE released_at IS NOT NULL)=1`).Scan(&protected); err != nil || !protected {
		t.Fatal("late result escaped changed configuration", err)
	}
	if h.personCalls.Load() != 1 || h.primary.count() != 0 {
		t.Fatal("late rejection reread person or fetched work details", h.personCalls.Load(), h.primary.count())
	}
	t.Log("M11_PERSON_LATE_DISABLE_PASS App=PRODUCTION Cookie=REAL physical_read=WAITED late_result=REJECTED release=RECORDED subscription=ZERO download=ZERO")
}
