package httpapi_test

import (
	"context"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
)

func publicUpgradeAppStarter(t *testing.T, f *publicUpgradeFixture) func() func() {
	t.Helper()
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.Database.URL = f.a.Config().Database.URL
	master, err := crypto.EnsureMasterKey(f.a.Config().DataDir, f.a.Config().SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SecretKey = hex.EncodeToString(master)
	if err = library.EnsureDefaultRoots(context.Background(), postgres.NewLibraryBootstrapStore(f.a.DBPool()), rootfs.NewFilesystem(), idgen.New(), cfg.DataDir); err != nil {
		t.Fatal(err)
	}
	f.exec(t, "INSERT INTO library_scan_schedule(root_id,next_run_at) SELECT id,clock_timestamp()+interval '1 hour' FROM library_root ON CONFLICT(root_id) DO UPDATE SET next_run_at=excluded.next_run_at")
	f.exec(t, "UPDATE indexer SET health_next_check_at=clock_timestamp()+interval '1 hour'")
	start := func() func() {
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		cfg.Server.HTTPAddr = listener.Addr().String()
		_ = listener.Close()
		startup := &runtimeTVStartup{ready: make(chan struct{})}
		a, e := app.New(cfg, app.WithLogger(logging.NewLogger(startup, "info")))
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case e := <-done:
					if e != nil {
						t.Error(e)
					}
				case <-time.After(20 * time.Second):
					t.Error("App drain timeout")
					_ = a.Close()
				}
			})
		}
		t.Cleanup(stop)
		select {
		case <-startup.ready:
		case e := <-done:
			t.Fatal(e)
		case <-time.After(20 * time.Second):
			t.Fatal("App startup timeout")
		}
		base := "http://" + cfg.Server.HTTPAddr
		f.runtimeURL = base
		client := &http.Client{Timeout: 5 * time.Second}
		f.router = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstream := r.Clone(r.Context())
			upstream.RequestURI = ""
			upstream.URL, _ = url.Parse(base + r.URL.RequestURI())
			upstream.Host = ""
			if r.Header.Get("Origin") == "http://"+r.Host {
				upstream.Header.Set("Origin", base)
			}
			response, e := client.Do(upstream)
			if e != nil {
				t.Error("formal App HTTP request failed", e)
				w.WriteHeader(502)
				return
			}
			defer func() { _ = response.Body.Close() }()
			for name, values := range response.Header {
				for _, v := range values {
					w.Header().Add(name, v)
				}
			}
			w.WriteHeader(response.StatusCode)
			if _, e = io.Copy(w, response.Body); e != nil {
				t.Error(e)
			}
		})
		return stop
	}
	return start
}

func TestPublicUpgradeCommandsAppCookieRestart(t *testing.T) {
	f := newPublicUpgradeFixture(t)
	start := publicUpgradeAppStarter(t, f)
	mode := f.enable(t)
	stop := start()
	body := f.reservationBody(t)
	files := f.unchangedFiles(t)
	first := f.command("upgrade-reservations", body, "app-original-reservation", f.admin, nil)
	if first.Code != 202 {
		t.Fatalf("App reserve %d %s", first.Code, first.Body.String())
	}
	f.noExecution(t)
	stop()
	stop = start()
	defer stop()
	replay := f.command("upgrade-reservations", body, "app-original-reservation", f.admin, nil)
	if replay.Code != 202 || replay.Body.String() != first.Body.String() {
		t.Fatalf("App receipt recovery %d %s", replay.Code, replay.Body.String())
	}
	replay = f.command("upgrade-modes", mode, "enable-upgrades", f.admin, nil)
	if replay.Code != 202 {
		t.Fatalf("App mode recovery %d %s", replay.Code, replay.Body.String())
	}
	if files != f.unchangedFiles(t) {
		t.Fatal("App reservation/restart modified original files or facts")
	}
	f.noExecution(t)
}
