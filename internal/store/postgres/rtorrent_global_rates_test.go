package postgres_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/rtorrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/downloadmetrics"
)

type rtorrentGlobalRateBuilder struct{}

func (rtorrentGlobalRateBuilder) NewDownloadGlobalRatesReader(source ports.DownloadGlobalRatesSource) (ports.DownloadGlobalRatesReader, error) {
	if source.ClientType != "rtorrent" {
		return nil, ports.ErrRejected
	}
	return rtorrent.NewAuthenticatedClient(source.URL, source.Username, source.Password, nil)
}

func TestRTorrentGlobalRatesPersistentPipelinePG(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		user, pass, ok := r.BasicAuth()
		if !ok || user != "reader" || pass != "owned-native-reader" {
			t.Error("original credential lost")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 4096))
		if e != nil {
			t.Error(e)
			return
		}
		value := "0"
		switch {
		case strings.Contains(string(raw), "<methodName>throttle.global_down.rate</methodName>"):
			value = "9007199254740993"
		case strings.Contains(string(raw), "<methodName>throttle.global_up.rate</methodName>"):
		default:
			t.Error("unexpected native operation")
			http.Error(w, "rejected", 400)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, "<methodResponse><params><param><value><i8>"+value+"</i8></value></param></params></methodResponse>")
	}))
	defer server.Close()
	f := newGlobalRateFixture(t)
	f.exec(t, `UPDATE download_client SET client_type='rtorrent',url=$2 WHERE id=$1`, f.client, server.URL)
	lease := f.claim(t)
	service := downloadmetrics.NewService(f.store, rtorrentGlobalRateBuilder{}, globalRateClock{})
	if err := service.Execute(t.Context(), lease); err != nil {
		t.Fatal("rTorrent collection failed", err)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected protocol traffic", calls.Load())
	}
	actor := globalRateAdmin(t, f)
	reader := postgres.NewDownloadGlobalRatesOverviewStore(f.pool)
	before := globalRateReadProtection(t, f)
	for range 2 {
		snapshot, err := reader.ReadDownloadGlobalRatesSnapshot(t.Context(), actor)
		if err != nil || snapshot == nil || len(snapshot.Sources) != 1 || len(snapshot.History) != 1 {
			t.Fatal(snapshot, err)
		}
		source := snapshot.Sources[0]
		if source.ClientType != "rtorrent" || source.Latest == nil || source.Latest.DownloadBytesPerSecond == nil || *source.Latest.DownloadBytesPerSecond != 9007199254740993 || source.Latest.UploadBytesPerSecond == nil || *source.Latest.UploadBytesPerSecond != 0 {
			t.Fatal("native64bit sample lost", source)
		}
	}
	if calls.Load() != 2 || globalRateReadProtection(t, f) != before {
		t.Fatal("read-only view changed state or triggered native request")
	}
}
