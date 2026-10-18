package app

import (
	"bytes"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
)

type verifiedRevisionHTTP struct {
	f       *organizationFixture
	server  *httptest.Server
	session string
	old     ports.TransferPlan
}

func verifiedRevisionServer(t *testing.T) *verifiedRevisionHTTP {
	t.Helper()
	f, service, _, old := revisionSetup(t)
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", f.root); err != nil {
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
	t.Cleanup(server.Close)
	return &verifiedRevisionHTTP{f: f, server: server, session: session, old: old}
}

func (f *verifiedRevisionHTTP) post(t *testing.T, suffix, key string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), "POST", f.server.URL+"/api/v1/transfer-plans/"+f.old.ID.String()+suffix, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", f.server.URL)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	request.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: f.session})
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16384))
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Cache-Control") != "private, no-store" && (response.StatusCode < 400 || response.Header.Get("Cache-Control") != "no-store") {
		t.Fatal("private response cache contract", response.Header.Get("Cache-Control"))
	}
	return response.StatusCode, data
}

func TestVerifiedActionPublicConfirmationPG(t *testing.T) {
	for _, action := range []string{"copy", "move"} {
		t.Run(action, func(t *testing.T) {
			f := verifiedRevisionServer(t)
			ctx := t.Context()
			original, err := f.f.q.GetTransferPlanByID(ctx, f.old.ID)
			if err != nil {
				t.Fatal(err)
			}
			originalSteps, err := f.f.q.ListStepsForPlan(ctx, f.old.ID)
			if err != nil {
				t.Fatal(err)
			}
			seed := filepath.Join(f.f.source, "public-seed.mkv")
			if err = os.Link(f.f.path, seed); err != nil {
				t.Fatal(err)
			}
			target := "Edited/Verified " + action + ".mkv"
			code, raw := f.post(t, "/revision-preview", "", map[string]any{"target_rel": target, "action": action})
			var preview ports.TransferRevisionPreview
			if code != 200 || json.Unmarshal(raw, &preview) != nil || !preview.Revisable || preview.Action != action {
				t.Fatalf("preview %d %s", code, raw)
			}
			body := map[string]any{"target_rel": target, "action": action, "expected_input_stamp": preview.InputStamp, "confirmed": true, "reason": "明确确认文件动作"}
			code, raw = f.post(t, "/revision", "public-verified-"+action, body)
			var receipt ports.TransferRevisionReceipt
			if code != 202 || json.Unmarshal(raw, &receipt) != nil {
				t.Fatalf("confirm %d %s", code, raw)
			}
			againCode, again := f.post(t, "/revision", "public-verified-"+action, body)
			if againCode != 202 || !bytes.Equal(again, raw) {
				t.Fatalf("lost receipt %d %s", againCode, again)
			}
			if f.f.count(t, "unit_fact") != 0 {
				t.Fatal("confirmation performed physical import")
			}
			job := f.f.claim(t, "public-verified-"+action)
			if err = f.f.svc.OrganizeDownload(ctx, job); err != nil {
				t.Fatal(err)
			}
			if f.f.count(t, "unit_fact") != 1 || f.f.count(t, "transfer_content_evidence") != 1 {
				t.Fatal("missing import or frozen content")
			}
			destination, err := os.Stat(filepath.Join(f.f.target, target))
			if err != nil {
				t.Fatal(err)
			}
			source, err := os.Stat(seed)
			if err != nil || os.SameFile(source, destination) {
				t.Fatal("destination must have its own inode", err)
			}
			before, err := os.ReadFile(seed)
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(filepath.Join(f.f.target, target))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("content mismatch", err)
			}
			_, sourceErr := os.Stat(f.f.path)
			if action == "move" && !os.IsNotExist(sourceErr) || action == "copy" && sourceErr != nil {
				t.Fatal("source outcome", sourceErr)
			}
			afterPlan, err := f.f.q.GetTransferPlanByID(ctx, f.old.ID)
			if err != nil || afterPlan != original {
				t.Fatal("original plan rewritten", err)
			}
			afterSteps, err := f.f.q.ListStepsForPlan(ctx, f.old.ID)
			if err != nil || !reflect.DeepEqual(originalSteps, afterSteps) {
				t.Fatal("original frozen steps rewritten", err)
			}
			restoredCode, restored := f.post(t, "/revision", "public-verified-"+action, body)
			if restoredCode != 202 || !bytes.Equal(restored, raw) {
				t.Fatalf("post-import recovery %d %s", restoredCode, restored)
			}
			body["action"] = "hardlink"
			if changed, _ := f.post(t, "/revision", "public-verified-"+action, body); changed != 409 {
				t.Fatal("key changed action", changed)
			}
		})
	}
}

func TestVerifiedActionPublicMaterialAndJSONGuardsPG(t *testing.T) {
	f := verifiedRevisionServer(t)
	code, raw := f.post(t, "/revision-preview", "", map[string]any{"target_rel": "Edited/Guard.mkv", "action": "copy"})
	var preview ports.TransferRevisionPreview
	if code != 200 || json.Unmarshal(raw, &preview) != nil || !preview.Revisable {
		t.Fatalf("preview %d %s", code, raw)
	}
	body := map[string]any{"target_rel": preview.TargetRel, "action": "move", "expected_input_stamp": preview.InputStamp, "confirmed": true, "reason": "明确确认动作"}
	if status, _ := f.post(t, "/revision", "wrong-material", body); status != 409 {
		t.Fatal("copy stamp authorized move", status)
	}
	for _, bad := range []map[string]any{
		{"target_rel": preview.TargetRel, "action": "automatic"},
		{"target_rel": preview.TargetRel, "action": "copy", "allow_overwrite": true},
	} {
		if status, _ := f.post(t, "/revision-preview", "", bad); status != 400 {
			t.Fatal("invalid or unknown field", status)
		}
	}
	if f.f.count(t, "transfer_plan") != 1 || f.f.count(t, "transfer_content_evidence") != 0 || f.f.count(t, "unit_fact") != 0 {
		t.Fatal("rejected command left effects")
	}
}
