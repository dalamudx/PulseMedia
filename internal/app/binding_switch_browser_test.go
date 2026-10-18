package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	usecasePolicy "github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestBindingSwitchCookieRecoveryAndPhysicalProtection(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit browser fixture required")
	}
	runFullMoviePipelineSource(t, true, "", "", func(f *organizationFixture, _ *ports.Actor, _ uuid.UUID, a *App) func() {
		ctx := context.Background()
		user := uuid.New()
		if _, e := f.q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "binding-cookie-owner", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
			t.Fatal(e)
		}
		actor := &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleMember}
		var policy string
		var indexer uuid.UUID
		if e := f.pool.QueryRow(ctx, "SELECT id FROM policy LIMIT 1").Scan(&policy); e != nil {
			t.Fatal(e)
		}
		if e := f.pool.QueryRow(ctx, "SELECT id FROM indexer LIMIT 1").Scan(&indexer); e != nil {
			t.Fatal(e)
		}
		eStore := postgres.NewSubscriptionIntentStore(f.pool, f.gen)
		sub, _, e := subscription.NewService(eStore, eStore, postgres.NewSubscriptionControlStore(f.pool, f.gen), eStore).CreateSubscription(ctx, actor, subscription.CreateSubscriptionCommand{MediaID: f.media, RootID: f.root, QualityPolicyID: policy, ScopeType: "all", DownloaderID: &f.client, IndexerIDs: []uuid.UUID{indexer}})
		if e != nil {
			t.Fatal(e)
		}
		paused, e := f.q.UpdateSubscriptionState(ctx, db.UpdateSubscriptionStateParams{ID: sub.ID, State: "paused"})
		if e != nil {
			t.Fatal(e)
		}
		sub, _, e = eStore.ReadSubscriptionDetail(ctx, actor, paused.ID)
		if e != nil {
			t.Fatal(e)
		}
		token, hash, e := crypto.GenerateToken("pm_sess_")
		if e != nil {
			t.Fatal(e)
		}
		_, csrf, e := crypto.GenerateToken("pm_csrf_")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.q.CreateSession(ctx, db.CreateSessionParams{ID: uuid.New(), UserID: user, TokenHash: hash, CsrfTokenHash: csrf, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); e != nil {
			t.Fatal(e)
		}
		authSvc := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
		searchSvc := usecaseSearch.NewService(postgres.NewSearchExecutionStore(f.pool), f.gen, crypto.NewSecretProtector(nil))
		searchSvc.SetHealthRepository(postgres.NewIndexerHealthStore(f.pool, f.gen))
		h := httpapi.NewServerHandler(a, authSvc, nil, nil, nil, nil, nil, usecasePolicy.NewService(postgres.NewPolicyStore(f.pool, f.gen)), searchSvc, nil, nil, nil, nil)
		h.SetSubscriptionBindingService(subscription.NewBindingService(postgres.NewSubscriptionBindingStore(f.pool, f.gen)))
		hub := httpapi.NewSSEHub()
		router := httpapi.NewRouter(a, h, authSvc, hub)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/fixture/admin" {
				if _, err := f.pool.Exec(r.Context(), "UPDATE app_user SET role='admin' WHERE id=$1", user); err != nil {
					w.WriteHeader(503)
				} else {
					w.WriteHeader(204)
				}
				return
			}
			if r.URL.Path == "/fixture/disable" || r.URL.Path == "/fixture/enable" {
				if _, e := f.pool.Exec(r.Context(), "UPDATE app_user SET is_active=$2 WHERE id=$1", user, r.URL.Path == "/fixture/enable"); e != nil {
					w.WriteHeader(503)
				} else {
					w.WriteHeader(204)
				}
				return
			}
			router.ServeHTTP(w, r)
		}))
		browserCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
		profile := filepath.Join(t.TempDir(), "chrome")
		chrome := exec.CommandContext(browserCtx, "/opt/google/chrome/chrome", "--headless=new", "--no-sandbox", "--disable-dev-shm-usage", "--remote-debugging-port=9222", "--user-data-dir="+profile)
		chrome.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		chrome.Cancel = func() error { return syscall.Kill(-chrome.Process.Pid, syscall.SIGKILL) }
		chrome.Stdout = io.Discard
		chrome.Stderr = io.Discard
		if e = chrome.Start(); e != nil {
			cancel()
			hub.Close()
			server.Close()
			t.Fatal(e)
		}
		cmd := exec.CommandContext(browserCtx, "node", "tests/binding-switch-browser.mjs")
		cmd.Dir = "../../web"
		cmd.Env = append(os.Environ(), "PULSE_BIND_BASE="+server.URL, "PULSE_BIND_SESSION="+token, "PULSE_BIND_SUB="+sub.ID.String(), "PULSE_BIND_VERSION="+strconv.FormatInt(sub.IntentVersion, 10))
		done := make(chan struct{})
		var output []byte
		var cmdErr error
		go func() { output, cmdErr = cmd.CombinedOutput(); close(done) }()
		t.Cleanup(func() { cancel(); <-done; _ = chrome.Wait(); hub.Close(); server.Close() })
		return func() {
			<-done
			if cmdErr != nil {
				t.Fatalf("binding browser: %v %s", cmdErr, output)
			}
			t.Log(string(output))
			current, e := f.q.GetSubscriptionByID(ctx, sub.ID)
			if e != nil || current.IntentVersion != sub.IntentVersion+1 || current.State != sub.State || current.ScopeType != sub.ScopeType || current.QualityPolicyRevision != sub.QualityPolicyRevision || current.DownloaderID.Valid || string(current.IndexerIds) != "[]" {
				t.Fatal("binding switch altered unrelated source", current, e)
			}
			var n int
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action='subscription.bindings_configured' AND target_id=$1", sub.ID.String()).Scan(&n); e != nil || n != 1 {
				t.Fatal("duplicate binding success", n, e)
			}
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM policy_revision WHERE policy_id='cookie_recovery'").Scan(&n); e != nil || n != 3 {
				t.Fatal("policy recovery duplicated immutable revisions", n, e)
			}
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM idempotency_record WHERE actor_id=$1 AND actor_type='user'", user.String()).Scan(&n); e != nil || n != 4 {
				t.Fatal("policy/binding recovery receipt count", n, e)
			}
		}
	})
}
