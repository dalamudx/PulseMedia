package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Only the external Torznab protocol peer is controlled. The actual App owns
// discovery scheduling, source workers, encrypted configuration and HTTP auth.
func TestTVEpisodeDiscoveryAppChromePG(t *testing.T) {
	f, _, _ := tvEpisodeDiscoveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	password := "MP31-owned-admin-password-42!"
	hash, e := crypto.HashPassword(password)
	if e != nil {
		t.Fatal(e)
	}
	execIntent(t, f.pool, `UPDATE app_user SET password_hash=$2 WHERE id=$1`, f.actor.ID, hash)
	var pages atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Path != "/api" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("t") == "caps" {
			_, _ = io.WriteString(w, `<caps><searching><search available="yes" supportedParams="q"/><tv-search available="yes" supportedParams="q,season,ep"/></searching></caps>`)
			return
		}
		if r.URL.Query().Get("q") != "Trusted Native Series 2024 S02E03" {
			_, _ = io.WriteString(w, `<rss version="2.0"><channel></channel></rss>`)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset != 0 && offset != 50 {
			http.Error(w, "invalid bounded offset", 400)
			return
		}
		if r.URL.Query().Get("apikey") != "mp31-owned-indexer-key" {
			http.Error(w, "fixture auth missing", http.StatusForbidden)
			return
		}
		pages.Add(1)
		count := 50
		if offset == 50 {
			count = 1
		}
		_, _ = io.WriteString(w, `<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel>`)
		_, _ = fmt.Fprintf(w, `<newznab:response offset="%d" total="51"/>`, offset)
		for i := range count {
			n := offset + i
			res := "1080p"
			if n == 50 {
				res = "2160p"
			}
			_, _ = fmt.Fprintf(w, `<item><title>Trusted.Native.Series.2024.S02E03.%s.WEB-DL.H264.mkv</title><guid>mp31-app-%d</guid><link>https://tv-source.test/%d</link><size>1234</size><torznab:attr name="size" value="1234"/><torznab:attr name="seeders" value="5"/></item>`, res, n, n)
		}
		_, _ = io.WriteString(w, `</channel></rss>`)
	}))
	defer peer.Close()
	encrypt := func(v string) []byte {
		t.Helper()
		raw, e := crypto.Encrypt(bytes.Repeat([]byte{27}, 32), []byte(v))
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	execIntent(t, f.pool, `UPDATE indexer SET url=$2,api_key_encrypted=$3,config_version=config_version+1 WHERE id=$1`, f.indexer, peer.URL, encrypt("mp31-owned-indexer-key"))
	execIntent(t, f.pool, `UPDATE indexer_configuration SET url_encrypted=$2,configuration_version=2 WHERE indexer_id=$1`, f.indexer, encrypt(peer.URL+"/api"))
	base, stop := startTVEpisodeUpgradeHTTPApp(t, ctx, f.pool.Config().ConnConfig.ConnString(), filepath.Join(t.TempDir(), "app-data"))
	defer stop()
	jar, e := cookiejar.New(nil)
	if e != nil {
		t.Fatal(e)
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		var input io.Reader
		if body != nil {
			raw, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			input = bytes.NewReader(raw)
		}
		req, e := http.NewRequestWithContext(ctx, method, base+path, input)
		if e != nil {
			t.Fatal(e)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", base)
		}
		response, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = response.Body.Close() }()
		raw, e := io.ReadAll(io.LimitReader(response.Body, 65536))
		if e != nil {
			t.Fatal(e)
		}
		if response.StatusCode != want {
			if strings.Contains(path, "/tv-episode-discovery/") {
				var sid string
				err := f.pool.QueryRow(ctx, `SELECT search_id::text FROM subscription_tv_episode_discovery WHERE subscription_id=$1 AND unit_id=$2`, f.sub, f.unit).Scan(&sid)
				if err == nil {
					id, parseErr := uuid.Parse(sid)
					if parseErr == nil {
						t.Logf("discovery current authorizer diagnostic: %v", postgres.AuthorizeTVEpisodeDiscoverySearch(ctx, db.New(f.pool), id))
					}
				} else {
					t.Logf("discovery schedule diagnostic: %v", err)
				}
			}
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.StatusCode, want, raw)
		}
		if strings.Contains(path, "discovery") && response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("discovery HTTP cacheable")
		}
		return raw
	}
	path := "/api/v1/subscriptions/" + f.sub.String() + "/tv-episode-discovery/" + f.unit.String()
	call(http.MethodGet, path, nil, http.StatusUnauthorized)
	call(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "creation-owner", "password": password}, http.StatusOK)
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var status ports.TVEpisodeDiscoveryStatus
	for {
		raw := call(http.MethodGet, path, nil, http.StatusOK)
		if e = json.Unmarshal(raw, &status); e != nil {
			t.Fatal(e)
		}
		if status.SearchID != nil && pages.Load() == 2 && status.State != "running" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("production discovery did not complete: %+v pages=%d", status, pages.Load())
		case <-tick.C:
		}
	}
	before := tvEpisodeDiscoveryState(t, f)
	calls := pages.Load()
	for range 3 {
		call(http.MethodGet, path, nil, http.StatusOK)
	}
	if pages.Load() != calls || tvEpisodeDiscoveryState(t, f) != before {
		t.Fatal("GET started search or changed business state")
	}
	if status.ExecutionAllowed || status.CurrentFileID == nil || *status.CurrentFileID != f.request.CurrentFileID {
		t.Fatal("unsafe App status", status)
	}
	command := exec.CommandContext(ctx, "node", "../../../web/tests/tv-episode-discovery-real-browser.mjs")
	// Let Go own browser scratch cleanup even when the regression runner has no UI env.
	command.Env = append(os.Environ(), "MP31_BACKEND="+base, "MP31_SUBSCRIPTION="+f.sub.String(), "MP31_UNIT="+f.unit.String(), "MP31_PASSWORD="+password, "PULSE_TEST_TEMP_ROOT="+t.TempDir())
	output, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("controlled Chrome: %v\n%s", e, output)
	}
	t.Log(string(output))
	if tvEpisodeDiscoveryState(t, f) != before {
		t.Fatal("Chrome comparison created authority or modified library")
	}
	stop()
	base, stop = startTVEpisodeUpgradeHTTPApp(t, ctx, f.pool.Config().ConnConfig.ConnString(), filepath.Join(t.TempDir(), "restart-data"))
	defer stop()
	call(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "creation-owner", "password": password}, http.StatusOK)
	raw := call(http.MethodGet, path, nil, http.StatusOK)
	var restarted ports.TVEpisodeDiscoveryStatus
	if json.Unmarshal(raw, &restarted) != nil || restarted.SearchID == nil || *restarted.SearchID != *status.SearchID {
		t.Fatal("restart lost original discovery", restarted)
	}
	if pages.Load() != calls || intentCount(t, f.pool, `SELECT count(*) FROM subscription_tv_episode_discovery_run`) != 1 {
		t.Fatal("restart bypassed cooldown")
	}
	t.Log("MP31 actual App/production worker + Cookie HTTP + Chrome + restart: two persisted pages, explicit TV Preview, no acquisition authority")
}
