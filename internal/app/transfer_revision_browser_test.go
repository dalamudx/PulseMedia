package app

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestManualMovieRevisionBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit owned browser fixture required")
	}
	for _, action := range []string{"hardlink", "copy", "move"} {
		t.Run(action, func(t *testing.T) {
			f, service, _, old := revisionSetup(t)
			ctx := t.Context()
			if action != "hardlink" {
				if _, err := f.pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", f.root); err != nil {
					t.Fatal(err)
				}
			}
			seed := filepath.Join(f.source, "revision-browser-seed.mkv")
			if err := os.Link(f.path, seed); err != nil {
				t.Fatal(err)
			}
			cfg := config.NewDefault()
			cfg.DataDir = t.TempDir()
			application, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = application.Close() })
			authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
			_, session, err := authService.Login(ctx, "revision-owner", "RevisionFixtureOwned123!")
			if err != nil {
				t.Fatal(err)
			}
			handler := httpapi.NewServerHandler(application, authService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			handler.SetTransferRevisionService(service)
			server := httptest.NewServer(httpapi.NewRouter(application, handler, authService, nil))
			defer server.Close()
			browserCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
			defer cancel()
			cdp := startOwnedRevisionChrome(t, browserCtx)
			command := exec.CommandContext(browserCtx, "node", "tests/transfer-revision-browser.mjs")
			command.Dir = "../../web"
			command.Env = append(os.Environ(), "PULSE_REVISION_UI_BASE="+server.URL, "PULSE_REVISION_UI_OP="+f.op.String(), "PULSE_REVISION_UI_PLAN="+old.ID.String(), "PULSE_REVISION_UI_SESSION="+session, "PULSE_REVISION_UI_CDP="+cdp, "PULSE_REVISION_UI_ACTION="+action)
			if dir := os.Getenv("PULSE_REVISION_UI_ARTIFACT_DIR"); dir != "" {
				command.Env = append(command.Env, "PULSE_REVISION_UI_SCREENSHOT="+filepath.Join(dir, "revision-"+action))
			}
			raw, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("real revision browser %v %s", err, raw)
			}
			var out struct {
				Key     string                        `json:"key"`
				Receipt ports.TransferRevisionReceipt `json:"receipt"`
				Body    ports.TransferRevisionCommand `json:"body"`
			}
			if json.Unmarshal(raw, &out) != nil || out.Key == "" || out.Receipt.PlanID == uuid.Nil || out.Receipt.PreviousPlanID != old.ID || out.Body.Action != action {
				t.Fatalf("missing original action/receipt %s", raw)
			}
			if f.count(t, "transfer_plan") != 2 || f.count(t, "unit_fact") != 0 {
				t.Fatal("browser queue treated as import")
			}
			job := f.claim(t, "revision-browser-worker")
			if err = f.svc.OrganizeDownload(ctx, job); err != nil {
				t.Fatal(err)
			}
			source, err := os.Stat(seed)
			if err != nil {
				t.Fatal("seed lost", err)
			}
			destination, err := os.Stat(filepath.Join(f.target, "Edited/Movie revised.mkv"))
			if err != nil || f.count(t, "unit_fact") != 1 {
				t.Fatal("browser target/import", err)
			}
			if os.SameFile(source, destination) != (action == "hardlink") {
				t.Fatal("wrong target inode for selected action")
			}
			sourceBytes, err := os.ReadFile(seed)
			if err != nil {
				t.Fatal(err)
			}
			targetBytes, err := os.ReadFile(filepath.Join(f.target, "Edited/Movie revised.mkv"))
			if err != nil || !bytes.Equal(sourceBytes, targetBytes) {
				t.Fatal("published content mismatch", err)
			}
			_, sourceErr := os.Stat(f.path)
			if action == "move" && !os.IsNotExist(sourceErr) || action != "move" && sourceErr != nil {
				t.Fatal("source outcome", sourceErr)
			}
			var current uuid.UUID
			if err = f.pool.QueryRow(ctx, "SELECT transfer_plan_id FROM download_task WHERE id=$1", f.taskID).Scan(&current); err != nil || current != out.Receipt.PlanID {
				t.Fatal("persisted successor mismatch", err)
			}
			t.Logf("REVISION_BROWSER_PASSED action=%s requests=2 imported=1 source_preserved=%t seed_preserved=true", action, action != "move")
		})
	}
}
