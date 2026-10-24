package httpapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
)

type m11TVDBAcceptance struct {
	f           *m5IndexerAcceptance
	primary     *m5CreationProtocol
	nativeCalls atomic.Int32
	tvdbCalls   atomic.Int32
	mode        atomic.Int32
}

func newM11TVDBAcceptance(t *testing.T) *m11TVDBAcceptance {
	t.Helper()
	f, p := newM5CreationAcceptance(t)
	h := &m11TVDBAcceptance{f: f, primary: p}
	native := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v4/") {
			h.tvdbCalls.Add(1)
			if r.URL.Path == "/v4/login" {
				var body map[string]string
				if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body["apikey"] != "owned-tvdb-key" || body["pin"] != "owned-tvdb-pin" || len(body) != 2 || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" {
					t.Error("native paired credential login mismatch")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = io.WriteString(w, `{"status":"success","data":{"token":"owned-tvdb-bearer"}}`)
				return
			}
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer owned-tvdb-bearer" {
				t.Error("native read authentication mismatch")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if h.mode.Load() == 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			switch r.URL.Path {
			case "/v4/series/81189/translations/zho":
				if r.URL.RawQuery != "" {
					t.Error("translation leaked query")
				}
				_, _ = io.WriteString(w, `{"status":"success","data":{"language":"zho","name":"备用中文名","overview":"备用中文简介"}}`)
			case "/v4/series/81189/episodes/official":
				if r.URL.Query().Get("season") != "1" || r.URL.Query().Get("page") != "0" || len(r.URL.Query()) != 2 {
					t.Error("official numbering or pagination changed")
				}
				_, _ = io.WriteString(w, `{"status":"success","data":{"series":{"id":81189},"episodes":[{"id":9007,"seriesId":81189,"seasonNumber":1,"number":107,"aired":"2030-01-01","absoluteNumber":207}]} ,"links":{"next":null,"total_items":1}}`)
			default:
				t.Error("unexpected native TVDB endpoint")
				w.WriteHeader(http.StatusBadRequest)
			}
			return
		}
		switch r.URL.Path {
		case "/3/tv/550/translations", "/3/tv/550/external_ids", "/3/find/9007", "/3/tv/550/season/1/episode/7":
			h.nativeCalls.Add(1)
			if r.Method != http.MethodGet || r.URL.Query().Get("api_key") == "" || r.Header.Get("Authorization") != "" {
				t.Error("TMDB cross-reference lost configured native auth")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch r.URL.Path {
			case "/3/tv/550/translations":
				if h.mode.Load() == 3 {
					_, _ = io.WriteString(w, `{"id":550,"translations":[{"iso_639_1":"zh","iso_3166_1":"CN","data":{"name":"Native sparse television","overview":"已有简介"}}]}`)
				} else {
					_, _ = io.WriteString(w, `{"id":550,"translations":[]}`)
				}
			case "/3/tv/550/external_ids":
				if h.mode.Load() == 1 {
					_, _ = io.WriteString(w, `{"id":550,"tvdb_id":null}`)
				} else {
					_, _ = io.WriteString(w, `{"id":550,"tvdb_id":81189}`)
				}
			case "/3/find/9007":
				if r.URL.Query().Get("external_source") != "tvdb_id" {
					t.Error("wrong external identity namespace")
				}
				_, _ = io.WriteString(w, `{"tv_episode_results":[{"id":5500107,"show_id":550,"season_number":1,"episode_number":7}]}`)
			case "/3/tv/550/season/1/episode/7":
				_, _ = io.WriteString(w, `{"id":5500107,"show_id":550,"season_number":1,"episode_number":7,"air_date":null}`)
			}
		default:
			p.serve(t, w, r)
		}
	}))
	t.Cleanup(native.Close)
	target, err := url.Parse(native.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr := native.Client().Transport
	p.mu.Lock()
	p.tvdbTransport = m11TVArtworkTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.User != nil || (r.URL.Host != "api.themoviedb.org" && r.URL.Host != "api4.thetvdb.com") {
			return nil, errors.New("unexpected owned metadata destination")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		clone.URL, clone.Host = &u, target.Host
		return tr.RoundTrip(clone)
	})
	p.mu.Unlock()
	h.setting(t, "tvdb.credentials", 0, map[string]any{"api_key": "owned-tvdb-key", "pin": "owned-tvdb-pin"}, "tvdb-credentials")
	h.setting(t, "tvdb.fallback", 0, map[string]any{"enabled": true}, "tvdb-enable")
	if h.tvdbCalls.Load() != 0 || h.nativeCalls.Load() != 0 {
		t.Fatal("saving configuration started upstream work")
	}
	var stored string
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT value::text FROM setting WHERE key='tvdb.credentials'`).Scan(&stored); err != nil || !strings.Contains(stored, "ciphertext") || strings.Contains(stored, "owned-tvdb-key") || strings.Contains(stored, "owned-tvdb-pin") {
		t.Fatal("paired settings not encrypted", err)
	}
	m9WordHTTPStable(t, f)
	return h
}
func (h *m11TVDBAcceptance) setting(t *testing.T, key string, version int, value any, command string) {
	t.Helper()
	response := h.f.request(t, http.MethodPut, "/api/v1/settings/"+key, command, map[string]any{"expected_version": version, "value": value}, h.f.session)
	m5IndexerStatus(t, response, http.StatusOK)
	if response.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("settings cache protection missing")
	}
	m11TVDBNoSecrets(t, response.body)
}
func m11TVDBNoSecrets(t *testing.T, body []byte) {
	t.Helper()
	for _, secret := range []string{"owned-tvdb-key", "owned-tvdb-pin", "owned-tvdb-bearer", "owned_tvdb_tail_failure", "ciphertext", "SQLSTATE", "pgx"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("response exposed private metadata configuration or storage error")
		}
	}
}
func (h *m11TVDBAcceptance) count() int {
	return h.primary.count() + int(h.nativeCalls.Load()+h.tvdbCalls.Load())
}

func TestM11TVDBTranslationCookieReadOnlyPG(t *testing.T) {
	h := newM11TVDBAcceptance(t)
	f := h.f
	path := "/api/v1/media/tmdb/tv/550"
	read := func() httpapi.TMDBTVDetailsResponse {
		t.Helper()
		response := f.request(t, http.MethodGet, path, "", nil, f.session)
		m5IndexerStatus(t, response, http.StatusOK)
		m11TVDBNoSecrets(t, response.body)
		var detail httpapi.TMDBTVDetailsResponse
		if err := json.Unmarshal(response.body, &detail); err != nil {
			t.Fatal(err)
		}
		return detail
	}
	before := m9MediaRequestState(t, f)
	translated := read()
	if translated.TmdbId != 550 || translated.Title != "备用中文名" || translated.Overview == nil || *translated.Overview != "备用中文简介" || translated.OriginalTitle == nil || *translated.OriginalTitle != "Native sparse television" || translated.EpisodesCount != 4 || translated.SeasonsCount != 3 || translated.TranslationSource == nil || translated.TranslationSource.SourceId != 81189 || len(translated.TranslationSource.Fields) != 2 {
		t.Fatal("public translation changed canonical identity, counts, or source", translated)
	}
	m9MediaRequestUnchanged(t, f, before)
	for _, mode := range []int32{1, 2, 3} {
		h.mode.Store(mode)
		current := read()
		if current.Title != "Native sparse television" || current.TranslationSource != nil {
			t.Fatal("unavailable/unnecessary translation replaced primary", mode, current)
		}
		m9MediaRequestUnchanged(t, f, before)
	}
	h.setting(t, "tvdb.fallback", 1, map[string]any{"enabled": false}, "tvdb-disable")
	m9WordHTTPStable(t, f)
	h.mode.Store(0)
	before = m9MediaRequestState(t, f)
	native := h.nativeCalls.Load()
	source := h.tvdbCalls.Load()
	current := read()
	if current.TranslationSource != nil || current.Title != "Native sparse television" || h.nativeCalls.Load() != native || h.tvdbCalls.Load() != source {
		t.Fatal("disabled fallback requested translations or TVDB")
	}
	m9MediaRequestUnchanged(t, f, before)
	m5InteractiveSQL(t, f, `UPDATE app_user SET is_active=false WHERE username='creation-admin'`)
	before = m9MediaRequestState(t, f)
	calls := h.count()
	denied := f.request(t, http.MethodGet, path, "", nil, f.session)
	m5IndexerStatus(t, denied, http.StatusUnauthorized)
	if h.count() != calls {
		t.Fatal("revoked session reached metadata")
	}
	m9MediaRequestUnchanged(t, f, before)
	t.Log("M11_TVDB_TRANSLATION_COOKIE_PASS App=PRODUCTION Cookie=REAL TLS=OWNED locale=EXPLICIT source=INDEPENDENT read_only=ALL_NON_AUTH facts=PRESERVED failure=PRIMARY_RETAINED disabled=NO_FALLBACK_IO permission=LIVE")
}

func TestM11TVDBRefreshCookieAtomicRecoveryPG(t *testing.T) {
	h := newM11TVDBAcceptance(t)
	f := h.f
	initialCalls := h.tvdbCalls.Load()
	creation := m5CreationPost(t, f, "tvdb-canonical-create", map[string]any{"tmdb_id": 550, "media_type": "tv"})
	media, sub := m5CreationID(t, creation, "media_id"), m5CreationID(t, creation, "id")
	if h.tvdbCalls.Load() != initialCalls {
		t.Fatal("display fallback entered canonical subscription creation")
	}
	original := m5CreationUnits(t, f, media, []string{"S01E01", "S01E03", "S02E01", "S02E03"})
	m5InteractiveSQL(t, f, `UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+interval '1 day'`)
	m9WordHTTPStable(t, f)
	path := "/api/v1/subscriptions/" + sub.String() + "/metadata/refresh"
	body := map[string]any{"expected_version": m5CreationVersion(t, f, sub), "seasons": []int{1}}
	m5InteractiveSQL(t, f, `CREATE FUNCTION tvdb_public_tail_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='tvdb-refresh-original' THEN RAISE EXCEPTION 'owned_tvdb_tail_failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER tvdb_public_tail_failure BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION tvdb_public_tail_failure()`)
	before := m9MediaRequestState(t, f)
	failed := f.request(t, http.MethodPost, path, "tvdb-refresh-original", body, f.session)
	m5IndexerStatus(t, failed, http.StatusServiceUnavailable)
	m11TVDBNoSecrets(t, failed.body)
	m9MediaRequestUnchanged(t, f, before)
	m5InteractiveSQL(t, f, `DROP TRIGGER tvdb_public_tail_failure ON idempotency_record; DROP FUNCTION tvdb_public_tail_failure()`)
	first := f.request(t, http.MethodPost, path, "tvdb-refresh-original", body, f.session)
	m5IndexerStatus(t, first, http.StatusOK)
	m11TVDBNoSecrets(t, first.body)
	var receipt httpapi.TVRefreshReceipt
	if json.Unmarshal(first.body, &receipt) != nil || receipt.ObservedEpisodes != 3 || first.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("supplement missing real refresh receipt")
	}
	units := m5CreationUnits(t, f, media, []string{"S01E01", "S01E03", "S01E07", "S02E01", "S02E03"})
	for key, id := range original {
		if units[key] != id {
			t.Fatal("existing canonical unit identity changed", key)
		}
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM media_unit u JOIN media_unit_identity i ON i.unit_id=u.id JOIN tvdb_episode_evidence e ON e.unit_id=u.id WHERE u.id=$1 AND u.air_date IS NULL AND u.absolute_number IS NULL AND i.namespace='tmdb.episode' AND i.external_id=5500107 AND i.tmdb_series_id=550 AND e.tvdb_episode_id=9007 AND e.tmdb_episode_id=5500107 AND e.tvdb_series_id=81189 AND e.episode_number=7 AND e.credential_version=1 AND e.fallback_version=1`, units["S01E07"]) != 1 {
		t.Fatal("unproven episode identity/date/ordinal or missing source evidence")
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM wanted_item WHERE subscription_id=$1`, sub) != 5 {
		t.Fatal("supplement did not atomically expand Wanted")
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM tv_season_metadata_snapshot WHERE media_id=$1 AND media_version=$2`, media, receipt.MediaVersion) != 0 {
		t.Fatal("supplement minted complete-season evidence")
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM download_task`) != 0 {
		t.Fatal("metadata refresh initiated download")
	}
	h.setting(t, "tvdb.fallback", 1, map[string]any{"enabled": false}, "tvdb-disable-after-refresh")
	m5InteractiveSQL(t, f, `UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+interval '1 day'`)
	m9WordHTTPStable(t, f)
	before = m9MediaRequestState(t, f)
	calls := h.count()
	replay := f.request(t, http.MethodPost, path, "tvdb-refresh-original", body, f.session)
	m5IndexerStatus(t, replay, http.StatusOK)
	if !bytes.Equal(replay.body, first.body) || h.count() != calls {
		t.Fatal("original-key recovery depended on current providers")
	}
	m9MediaRequestUnchanged(t, f, before)
	later := f.request(t, http.MethodPost, path, "tvdb-primary-later", body, f.session)
	m5IndexerStatus(t, later, http.StatusOK)
	if m5InteractiveInt(t, f, `SELECT count(*) FROM media_unit WHERE id=$1 AND NOT retired`, units["S01E07"]) != 1 || m5InteractiveInt(t, f, `SELECT count(*) FROM tvdb_episode_evidence WHERE unit_id=$1`, units["S01E07"]) != 1 {
		t.Fatal("later primary omission retired independent proven unit")
	}
	t.Log("M11_TVDB_REFRESH_COOKIE_PASS App=PRODUCTION Cookie=REAL TLS=OWNED TMDB=CANONICAL TVDB=INDEPENDENT source_ordinal=107 canonical_ordinal=7 dates=UNKNOWN Wanted=ATOMIC tail=FULL_ROLLBACK original_key=READ_ONLY later_omission=PRESERVED downloads=0")
}
