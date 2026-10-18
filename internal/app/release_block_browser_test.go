package app

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	search "github.com/dalamudx/PulseMedia/internal/usecase/search"
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

func TestReleaseBlockScopeCookieAndPhysicalProtection(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit browser fixture required")
	}
	runFullMoviePipelineSource(t, true, "", "", func(f *organizationFixture, _ *ports.Actor, _ uuid.UUID, a *App) func() {
		ctx := context.Background()
		user := uuid.New()
		if _, e := f.q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "block-cookie-owner", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
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
		sub, wanted, e := subscription.NewService(eStore, eStore, postgres.NewSubscriptionControlStore(f.pool, f.gen), eStore).CreateSubscription(ctx, actor, subscription.CreateSubscriptionCommand{MediaID: f.media, RootID: f.root, QualityPolicyID: policy, QualityPolicyRevision: 1, ScopeType: "all", DownloaderID: &f.client, IndexerIDs: []uuid.UUID{indexer}})
		if e != nil {
			t.Fatal(e)
		}
		paused, e := f.q.UpdateSubscriptionState(ctx, db.UpdateSubscriptionStateParams{ID: sub.ID, State: "paused"})
		if e != nil {
			t.Fatal(e)
		}
		sub, _, e = eStore.ReadSubscriptionDetail(ctx, actor, paused.ID)
		if e != nil || len(wanted) != 1 {
			t.Fatal(e)
		}
		candidate, round := uuid.New(), uuid.New()
		if e = f.pool.QueryRow(ctx, "SELECT id FROM release_candidate LIMIT 1").Scan(&candidate); e != nil {
			t.Fatal(e)
		}
		stamp, _ := json.Marshal(acquisition.RoundSnapshot{RootID: f.root, UnitID: f.unit, MediaID: f.media, PolicyID: policy, Indexers: map[string]int64{indexer.String(): 1}})
		ids, _ := json.Marshal([]uuid.UUID{candidate})
		if _, e = f.pool.Exec(ctx, `INSERT INTO search_round(id,subscription_id,wanted_item_id,snapshot_intent_version,snapshot_policy_revision,state,deadline,candidate_ids,decision_snapshot,created_at) VALUES($1,$2,$3,$4,$5,'aborted',clock_timestamp()+interval '10 minute',$6,$7,clock_timestamp())`, round, sub.ID, wanted[0].ID, sub.IntentVersion, sub.QualityPolicyRevision, ids, stamp); e != nil {
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
		if _, e = f.q.CreateSession(ctx, db.CreateSessionParams{ID: uuid.New(), UserID: uuid.MustParse(actor.ID), TokenHash: hash, CsrfTokenHash: csrf, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); e != nil {
			t.Fatal(e)
		}
		authSvc := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
		h := httpapi.NewServerHandler(a, authSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		h.SetControlService(subscription.NewControlService(postgres.NewSubscriptionControlStore(f.pool, f.gen)))
		h.SetCandidateExplanationService(search.NewCandidateExplanationService(postgres.NewCandidateExplanationStore(f.pool)))
		hub := httpapi.NewSSEHub()
		router := httpapi.NewRouter(a, h, authSvc, hub)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/fixture/disable" || r.URL.Path == "/fixture/enable" {
				if _, e := f.pool.Exec(r.Context(), "UPDATE app_user SET is_active=$2 WHERE id=$1", uuid.MustParse(actor.ID), r.URL.Path == "/fixture/enable"); e != nil {
					w.WriteHeader(503)
				} else {
					w.WriteHeader(204)
				}
				return
			}
			router.ServeHTTP(w, r)
		}))
		browserCtx, cancel := context.WithTimeout(ctx, 80*time.Second)
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
		cmd := exec.CommandContext(browserCtx, "node", "tests/release-block-browser.mjs")
		cmd.Dir = "../../web"
		cmd.Env = append(os.Environ(), "PULSE_BLOCK_BASE="+server.URL, "PULSE_BLOCK_SESSION="+token, "PULSE_BLOCK_SUB="+sub.ID.String(), "PULSE_BLOCK_CANDIDATE="+candidate.String(), "PULSE_BLOCK_VERSION="+strconv.FormatInt(sub.IntentVersion, 10))
		done := make(chan struct{})
		var output []byte
		var cmdErr error
		go func() { output, cmdErr = cmd.CombinedOutput(); close(done) }()
		t.Cleanup(func() { cancel(); <-done; _ = chrome.Wait(); hub.Close(); server.Close() })
		return func() {
			<-done
			if cmdErr != nil {
				t.Fatalf("release block browser: %v %s", cmdErr, output)
			}
			t.Log(string(output))
			var active, records, version, controls int
			if e = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM release_block WHERE subscription_id=$1 AND blocked),(SELECT count(*) FROM release_block WHERE subscription_id=$1),(SELECT intent_version FROM subscription WHERE id=$1),(SELECT count(*) FROM audit_log WHERE action='subscription.control_release_block')`, sub.ID).Scan(&active, &records, &version, &controls); e != nil || active != 0 || records != 1 || int64(version) != sub.IntentVersion+6 || controls != 2 {
				t.Fatal("block/scope receipt drift", active, records, version, controls, e)
			}
		}
	})
}
