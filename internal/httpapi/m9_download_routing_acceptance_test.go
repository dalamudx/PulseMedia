package httpapi_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestM9AutomaticTVRoutingNativeHTTPPG(t *testing.T) {
	var wrongAdds atomic.Int32
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/torrents/add" {
			wrongAdds.Add(1)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(wrong.Close)
	var target, source, decoy uuid.UUID
	h := newM5AutomaticTVAcceptance(t, false, func(cfg *config.Config) {
		if target != uuid.Nil {
			cfg.DownloadRouting.Rules = []config.DownloadRoutingRule{{MediaType: "tv", DownloaderID: decoy}, {IndexerID: source, DownloaderID: target}}
		}
	})
	memberCookie := h.f.session
	admin := m5CreationLogin(t, h.f, "creation-admin")
	response := h.f.request(t, http.MethodPost, "/api/v1/download-clients", "", map[string]any{"name": "000 TV dedicated", "client_type": "qbittorrent", "url": wrong.URL, "username": "owned-decoy", "password": "owned-decoy-password", "remote_path_map": map[string]string{}}, admin)
	m5IndexerStatus(t, response, http.StatusCreated)
	target, source, decoy = h.client, h.indexer, m5CreationID(t, response, "id")
	h.f.session = memberCookie
	h.f.stopRuntime()
	a, err := h.f.newRuntime()
	if err != nil {
		t.Fatal("configured replacement App failed", err)
	}
	h.f.app = a
	done := m8AutomaticTVWatchStart(t, a)
	client := &http.Client{Timeout: time.Second}
	t.Cleanup(client.CloseIdleConnections)
	m5AutomaticTVWait(t, 10*time.Second, "configured App readiness", func(ctx context.Context) (bool, error) {
		select {
		case <-done:
			return false, errors.New("configured App exited")
		default:
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.f.base+"/api/v1/auth/status", nil)
		if err != nil {
			return false, err
		}
		response, err := client.Do(request)
		if err != nil {
			return false, nil
		}
		defer func() { _ = response.Body.Close() }()
		return response.StatusCode == http.StatusOK, nil
	})
	delete(h.body, "downloader_id")
	receipt := m5CreationPost(t, h.f, "m5-automatic-tv-original", h.body)
	h.subscription = m5CreationID(t, receipt, "id")
	defer func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var state []byte
		err := h.f.app.DBPool().QueryRow(ctx, `SELECT jsonb_build_object('bound',s.downloader_id IS NOT NULL,'intent',s.intent_version,'wanted',(SELECT jsonb_agg(jsonb_build_object('unit',u.unit_key,'intent',w.intent_version,'satisfied',w.is_satisfied,'next_future',w.next_evaluate_at>clock_timestamp())) FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id WHERE w.subscription_id=s.id),'rounds',(SELECT jsonb_agg(r.state) FROM automatic_tv_round r WHERE r.subscription_id=s.id),'jobs',(SELECT jsonb_agg(jsonb_build_object('kind',j.kind,'state',j.state)) FROM job j),'sources',(SELECT jsonb_agg(jsonb_build_object('state',x.state,'query_count',(SELECT count(*) FROM automatic_tv_query y WHERE y.round_id=x.round_id))) FROM automatic_tv_source x JOIN automatic_tv_round r ON r.id=x.round_id WHERE r.subscription_id=s.id),'queries',(SELECT jsonb_agg(jsonb_build_object('type',i.query_type,'state',i.state,'failure',CASE WHEN i.error IN ('unavailable','bad_response','unsupported','auth','timeout','rate_limited') THEN i.error ELSE CASE WHEN i.error IS NULL THEN 'none' ELSE 'other' END END)) FROM indexer_query i),'route_audits',(SELECT count(*) FROM audit_log WHERE action='subscription.downloader_routed' AND target_id=s.id::text)) FROM subscription s WHERE s.id=$1`, h.subscription).Scan(&state)
		t.Logf("routing stages bound_state=%s diagnostic_read_ok=%t source_reads=%d Add=%d info=%d files=%d wrong_Add=%d", state, err == nil, h.searches.Load(), h.adds.Load(), h.infoReads.Load(), h.fileReads.Load(), wrongAdds.Load())
	}()
	if err = a.DBPool().QueryRow(t.Context(), `SELECT media_id FROM subscription WHERE id=$1 AND user_id=$2`, h.subscription, h.member).Scan(&h.media); err != nil {
		t.Fatal("real member must own the unbound subscription", err)
	}
	units := m5CreationUnits(t, h.f, h.media, []string{"S01E01", "S01E03", "S01E07", "S02E09"})
	h.unit = units["S01E03"]
	m5AutomaticTVWait(t, 90*time.Second, "native Add or source outcome", func(ctx context.Context) (bool, error) {
		if h.adds.Load() > 0 {
			return true, nil
		}
		var failed bool
		if err := a.DBPool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM automatic_tv_source x JOIN automatic_tv_round r ON r.id=x.round_id WHERE r.subscription_id=$1 AND x.state IN ('failed','timeout','deferred'))`, h.subscription).Scan(&failed); err != nil {
			return false, errors.New("source status read failed")
		}
		if failed {
			return false, errors.New("source reached a terminal outcome before Add")
		}
		return false, nil
	})
	op := h.awaitAdd(t)
	if h.count(t, `SELECT count(*) FROM subscription s JOIN wanted_item w ON w.subscription_id=s.id WHERE s.id=$1 AND s.downloader_id=$2 AND w.intent_version=s.intent_version`, h.subscription, h.client) != 4 {
		t.Fatal("automatic binding did not refresh every sparse Wanted")
	}
	if h.count(t, `SELECT count(*) FROM audit_log WHERE action='subscription.downloader_routed' AND target_id=$1`, h.subscription.String()) != 1 || h.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='subscription.downloader_routed' AND aggregate_id=$1`, h.subscription.String()) != 1 {
		t.Fatal("first automatic binding was not unique")
	}
	h.complete.Store(true)
	h.unblockAdd()
	h.awaitImported(t, op, 100*time.Second)
	h.assertImported(t, op, receipt)
	if wrongAdds.Load() != 0 {
		t.Fatal("App submitted to the misleading media route")
	}
	t.Log("M9_TV_NATIVE_ROUTING_APP_PASS Cookie=REAL selection=AUTOMATIC binding=ATOMIC site_over_media=true original_bytes=true Add=1 wrong_Add=0 imported=1 same_inode=true original_key=true")
}
