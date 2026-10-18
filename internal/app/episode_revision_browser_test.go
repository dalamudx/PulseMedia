package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	transfer "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func runEpisodeRevisionCookieBrowser(t *testing.T, ctx context.Context, f *organizationFixture, repo *transferstore.Repository, actor *ports.Actor, planID uuid.UUID, target string) (*ports.TransferRevisionReceipt, ports.TransferRevisionCommand, string) {
	t.Helper()
	pool, e := postgres.NewPool(ctx, f.pool.Config().ConnConfig.ConnString(), postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	a, e := New(cfg, WithDBPool(pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = a.Close() }()
	token, hash, e := crypto.GenerateToken("pm_sess_")
	if e != nil {
		t.Fatal(e)
	}
	_, csrf, e := crypto.GenerateToken("pm_csrf_")
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.New(pool).CreateSession(ctx, db.CreateSessionParams{ID: uuid.New(), UserID: uuid.MustParse(actor.ID), TokenHash: hash, CsrfTokenHash: csrf, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if e != nil {
		t.Fatal(e)
	}
	authSvc := auth.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	h := httpapi.NewServerHandler(a, authSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetTransferRevisionService(transfer.NewRevisionService(repo))
	server := httptest.NewServer(httpapi.NewRouter(a, h, authSvc, nil))
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	cmd := exec.CommandContext(browserCtx, "node", "tests/transfer-revision-browser.mjs")
	cmd.Dir = "../../web"
	cmd.Env = append(os.Environ(), "PULSE_REVISION_UI_BASE="+server.URL, "PULSE_REVISION_UI_OP="+f.op.String(), "PULSE_REVISION_UI_PLAN="+planID.String(), "PULSE_REVISION_UI_SESSION="+token, "PULSE_REVISION_UI_TARGET="+target, "PULSE_REVISION_UI_UNIT=S01E01", "PULSE_REVISION_UI_CDP="+cdp, "PULSE_REVISION_UI_ACTION=hardlink")
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("real episode revision browser %v %s", e, raw)
	}
	var out struct {
		Key     string                        `json:"key"`
		Receipt ports.TransferRevisionReceipt `json:"receipt"`
		Body    ports.TransferRevisionCommand `json:"body"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Key == "" || out.Receipt.PreviousPlanID != planID || out.Receipt.PlanID == planID || out.Body.TargetRel != target {
		t.Fatal("episode browser receipt missing")
	}
	out.Body.PlanID = planID
	return &out.Receipt, out.Body, out.Key
}
