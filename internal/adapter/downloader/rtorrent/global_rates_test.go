package rtorrent_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/rtorrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRTorrentGlobalRatesAndSeedingReadOnly(t *testing.T) {
	_, hash := nativeMetainfo()
	const tag = "pulsemedia:op:12345678-1234-1234-1234-123456789abc"
	var methods atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := readCall(t, r)
		methods.Add(1)
		var value any
		switch call.Method {
		case "throttle.global_down.rate":
			value = int64(9007199254740993)
		case "throttle.global_up.rate":
			value = int64(0)
		case "d.multicall2":
			row := nativeRow(hash)
			row[6] = 1024
			row[8] = 1
			row[12] = "pulsemedia," + tag
			value = []any{append(row, int64(1250))}
		default:
			t.Error("unexpected RPC", call.Method)
			http.Error(w, "unavailable", 500)
			return
		}
		_, _ = w.Write([]byte(response(value)))
	}))
	defer server.Close()
	client := rtorrent.NewClient(server.URL, server.Client())
	rates, err := client.ReadGlobalRates(context.Background())
	if err != nil || rates.DownloadBytesPerSecond == nil || *rates.DownloadBytesPerSecond != 9007199254740993 || rates.UploadBytesPerSecond == nil || *rates.UploadBytesPerSecond != 0 {
		t.Fatal(rates, err)
	}
	task, err := client.SeedingObserver().ReadSeedingTorrent(context.Background(), hash, tag)
	if err != nil || !task.Facts.Complete || task.Facts.ShareRatio == nil || *task.Facts.ShareRatio != 1.25 || task.Facts.SeedingSeconds != nil {
		t.Fatal(task, err)
	}
	free, err := client.SeedingObserver().ReadSeedingFreeSpace(context.Background(), nativeDirectory)
	if err != nil || free.FreeBytes != nil {
		t.Fatal(free, err)
	}
	if methods.Load() != 3 {
		t.Fatal("unexpected external calls", methods.Load())
	}
}

func TestRTorrentRatesFailureDoesNotBecomeZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response(int64(-1)))) }))
	defer server.Close()
	rates, err := rtorrent.NewClient(server.URL, server.Client()).ReadGlobalRates(context.Background())
	if !errors.Is(err, ports.ErrBadResponse) || rates.DownloadBytesPerSecond != nil || rates.UploadBytesPerSecond != nil {
		t.Fatal(rates, err)
	}
}
