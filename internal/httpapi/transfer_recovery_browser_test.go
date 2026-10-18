package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

type recoveryBrowserResult struct {
	Key     string                        `json:"key"`
	Receipt ports.TransferRecoveryReceipt `json:"receipt"`
}

func TestOriginalTransferRecoveryBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	reviewedEpisodeAcquisitionProbe(t, false, false, false, false, true)
}
func runOriginalTransferRecoveryBrowser(t *testing.T, f *episodeMappingFixture, op, plan uuid.UUID) recoveryBrowserResult {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Log("UI_REQUEST_BEGIN", r.Method, r.URL.Path)
		defer t.Log("UI_REQUEST_END", r.Method, r.URL.Path)
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "tests/transfer-recovery-browser.mjs")
	cmd.Dir = "../../web"
	cmd.Env = append(os.Environ(), "PULSE_RECOVERY_UI_BASE="+server.URL, "PULSE_RECOVERY_UI_OP="+op.String(), "PULSE_RECOVERY_UI_PLAN="+plan.String())
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("real original recovery browser: %v\n%s", e, raw)
	}
	var out recoveryBrowserResult
	if json.Unmarshal(raw, &out) != nil || out.Receipt.PlanID != plan || out.Receipt.JobID == uuid.Nil || out.Key == "" {
		t.Fatalf("browser missing actual receipt: %s", raw)
	}
	return out
}
