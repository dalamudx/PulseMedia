package download_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func TestDownloaderNativeFactoryBudgetLimitedToTwo(t *testing.T) {
	mgr, queries, key, clientID, _ := setupDownloadTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var active, peak, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/auth/login" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- struct{}{}
		<-release
		_, _ = fmt.Fprint(w, "Ok.")
	}))
	defer server.Close()
	cipher, err := crypto.Encrypt(key, []byte("synthetic-budget-credential"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = queries.UpdateDownloadClient(ctx, db.UpdateDownloadClientParams{ID: clientID, Name: "native budget fixture", ClientType: "qbittorrent", Url: server.URL, Username: "fixture", PasswordEncrypted: cipher}); err != nil {
		t.Fatal(err)
	}
	mgr.SetClientFactory(func(kind, endpoint, user, secret string) (ports.DownloadClient, error) {
		return qbittorrent.NewClient(endpoint, user, secret, server.Client())
	})
	done := make(chan error, 8)
	for range 8 {
		go func() { _, _, e := mgr.TestClient(ctx, clientID); done <- e }()
	}
	defer func() {
		close(release)
		for range 8 {
			if e := <-done; e != nil {
				t.Errorf("native request: %v", e)
			}
		}
		if calls.Load() != 8 {
			t.Errorf("calls=%d", calls.Load())
		}
		if peak.Load() > 2 {
			t.Errorf("physical concurrency=%d", peak.Load())
		}
	}()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("native clients did not start")
		}
	}
	select {
	case <-entered:
		t.Error("third native downloader request bypassed physical budget")
	case <-time.After(60 * time.Millisecond):
	}
}
