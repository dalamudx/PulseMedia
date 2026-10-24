package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

func preparePublicUpgradePhysicalFiles(t *testing.T, f *publicUpgradeFixture) (string, string) {
	t.Helper()
	root, source := t.TempDir(), t.TempDir()
	if e := os.WriteFile(filepath.Join(root, ".pulsemedia_root"), []byte("root"), 0600); e != nil {
		t.Fatal(e)
	}
	old := filepath.Join(root, "original.mkv")
	if e := os.WriteFile(old, bytes.Repeat([]byte("o"), 100), 0600); e != nil {
		t.Fatal(e)
	}
	stat, e := os.Stat(old)
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `UPDATE library_root SET path=$2,marker_filename='.pulsemedia_root',health='online',baseline_completed=true WHERE id=$1`, f.root, root)
	f.exec(t, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1`, f.root)
	mapping, _ := json.Marshal(map[string]string{root: source})
	f.exec(t, `UPDATE download_client SET remote_path_map=$2 WHERE id=$1`, f.client, mapping)
	fingerprint := fmt.Sprintf("stat.v1:%d:%d:%d", stat.Sys().(*syscall.Stat_t).Ino, stat.Size(), stat.ModTime().UnixNano())
	f.exec(t, `UPDATE library_file SET size_bytes=$2,mtime_ns=$3,file_fingerprint=$4 WHERE id=$1`, f.request.CurrentFileID, stat.Size(), stat.ModTime().UnixNano(), fingerprint)
	f.exec(t, `UPDATE unit_fact SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE id=$1`, f.fact)
	var fresh uuid.UUID
	if e = f.a.DBPool().QueryRow(context.Background(), `INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile) SELECT gen_random_uuid(),unit_id,'imported',source_file_id,root_id,quality_profile FROM unit_fact WHERE id=$1 RETURNING id`, f.fact).Scan(&fresh); e != nil {
		t.Fatal(e)
	}
	f.fact = fresh
	f.project(t)
	if e = os.WriteFile(filepath.Join(source, "Fight.Club.1999.2160p.Bluray.x265.mkv"), bytes.Repeat([]byte("v"), 120), 0600); e != nil {
		t.Fatal(e)
	}
	return root, source
}
func publicUpgradeOrganizationState(t *testing.T, f *publicPreparationFixture, sub uuid.UUID) (*ports.MovieUpgradeOrganizationState, int) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+sub.String()+"/upgrade-organizations/"+f.operation.String(), nil)
	r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: f.admin})
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		return nil, w.Code
	}
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("missing no-store")
	}
	var out ports.MovieUpgradeOrganizationState
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if out.OperationID != f.operation || out.ExecutionAllowed {
		t.Fatal(out)
	}
	return &out, w.Code
}
func publicUpgradeOriginalFileState(t *testing.T, f *publicPreparationFixture) string {
	t.Helper()
	var out string
	e := f.a.DBPool().QueryRow(context.Background(), `SELECT jsonb_build_array((SELECT to_jsonb(l) FROM library_file l WHERE id=$1),(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x WHERE source_file_id=$1),(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.unit_id) FROM library_file_unit x WHERE file_id=$1))::text`, f.request.CurrentFileID).Scan(&out)
	if e != nil {
		t.Fatal(e)
	}
	return out
}

// Queue through the production Cookie/CSRF route; return a post-restart assertion
// that checks ingestion recovery and all three original command receipts.
func exercisePublicUpgradeOrganization(t *testing.T, f *publicPreparationFixture) func() {
	t.Helper()
	original := publicUpgradeOriginalFileState(t, f)
	state, code := publicUpgradeOrganizationState(t, f, f.request.SubscriptionID)
	if code != 200 || state.Status != "organization_review_required" {
		t.Fatal(state, code)
	}
	if _, code = publicUpgradeOrganizationState(t, f, uuid.New()); code != 404 {
		t.Fatal("foreign subscription status", code)
	}
	previewBody := fmt.Sprintf(`{"operation_id":%q}`, f.operation)
	malformed := previewBody[:len(previewBody)-1] + `,"Operation_ID":"` + f.operation.String() + `"}`
	if w := f.command("upgrade-organization-previews", malformed, "", f.admin, nil); w.Code != 400 {
		t.Fatal("alias accepted", w.Code)
	}
	w := f.command("upgrade-organization-previews", previewBody, "", f.admin, nil)
	if w.Code != 200 {
		t.Fatal("organization preview", w.Code, w.Body.String())
	}
	var review ports.MovieUpgradeOrganizationReview
	if e := json.Unmarshal(w.Body.Bytes(), &review); e != nil {
		t.Fatal(e)
	}
	issuer := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.a.DBPool(), idgen.New()), crypto.ReadTokenIssuer{}, f.a.Clock())
	_, readToken, err := issuer.Create(context.Background(), f.actor, ports.TokenCommand{Name: "organization read"}, "organization-read-token")
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]string{}
	receipts := map[string]string{}
	command := func(endpoint string, v any, out any) {
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		bodies[endpoint] = string(raw)
		for _, denied := range []struct {
			name, cookie string
			edit         func(*http.Request)
			want         int
		}{
			{"anonymous", "", nil, 401},
			{"member", f.member, nil, 403},
			{"API token", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+readToken) }, 403},
			{"cross origin", f.admin, func(r *http.Request) { r.Header.Set("Origin", "https://outside.invalid") }, 403},
			{"missing key", f.admin, func(r *http.Request) { r.Header.Del("Idempotency-Key") }, 400},
			{"duplicate key", f.admin, func(r *http.Request) { r.Header.Add("Idempotency-Key", "second") }, 400},
		} {
			got := f.command(endpoint, string(raw), "org-denied-"+endpoint, denied.cookie, denied.edit)
			if got.Code != denied.want {
				t.Fatalf("%s %s: got %d want %d", endpoint, denied.name, got.Code, denied.want)
			}
		}
		for _, invalid := range []string{
			strings.Replace(string(raw), `"confirmed":true`, `"confirmed":true,"confirmed":true`, 1),
			strings.Replace(string(raw), `"confirmed":true`, `"confirmed":false`, 1),
			strings.TrimSuffix(string(raw), "}") + `,"execution_allowed":true}`,
		} {
			if got := f.command(endpoint, invalid, "org-invalid-"+endpoint, f.admin, nil); got.Code != 400 {
				t.Fatal(endpoint, "invalid body", got.Code)
			}
		}
		wrong := f.command(endpoint, string(raw), "org-app-"+endpoint, f.admin, func(r *http.Request) {
			r.URL.Path = strings.Replace(r.URL.Path, f.request.SubscriptionID.String(), uuid.NewString(), 1)
		})
		if wrong.Code != 404 {
			t.Fatal("foreign subscription command", endpoint, wrong.Code, wrong.Body.String())
		}
		got := f.command(endpoint, string(raw), "org-app-"+endpoint, f.admin, nil)
		if got.Code != 202 {
			t.Fatal(endpoint, got.Code, got.Body.String())
		}
		receipts[endpoint] = got.Body.String()
		if e = json.Unmarshal(got.Body.Bytes(), out); e != nil {
			t.Fatal(e)
		}
	}
	var authorized ports.MovieUpgradeOrganizationAuthorization
	command("upgrade-organization-authorizations", ports.AuthorizeMovieUpgradeOrganization{OperationID: f.operation, ObservationID: review.ObservationID, InputStamp: review.InputStamp, Confirmed: true}, &authorized)
	var prepared ports.MovieUpgradeOrganizationPreparation
	command("upgrade-organization-preparations", ports.PrepareMovieUpgradeOrganization{OperationID: f.operation, AuthorizationID: authorized.ID, Confirmed: true}, &prepared)
	var queued ports.MovieUpgradeOrganizationJobReceipt
	command("upgrade-organization-jobs", ports.QueueMovieUpgradeOrganization{OperationID: f.operation, PreparationID: prepared.ID, Confirmed: true}, &queued)
	if authorized.ExecutionAllowed || prepared.ExecutionAllowed || queued.ExecutionAllowed {
		t.Fatal("receipt granted execution")
	}
	waitPublicUpgradeObservation(t, func() bool {
		var yes bool
		e := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_organization_execution_result WHERE job_id=$1 AND status='linked_pending_ingestion')`, queued.JobID).Scan(&yes)
		if e != nil {
			t.Fatal(e)
		}
		return yes
	})
	return func() {
		waitPublicUpgradeObservation(t, func() bool {
			state, code := publicUpgradeOrganizationState(t, f, f.request.SubscriptionID)
			return code == 200 && state.Status == "imported_pending_retirement" && state.NewFileID != nil
		})
		if publicUpgradeOriginalFileState(t, f) != original {
			t.Fatal("old file or facts changed before retirement")
		}
		for endpoint, body := range bodies {
			w := f.command(endpoint, body, "org-app-"+endpoint, f.admin, nil)
			if w.Code != 202 || w.Body.String() != receipts[endpoint] {
				t.Fatal("receipt replay", endpoint, w.Code, w.Body.String())
			}
		}
		var source, target string
		e := f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT value FROM download_client c,jsonb_each_text(c.remote_path_map) WHERE c.id=$2 LIMIT 1)||'/Fight.Club.1999.2160p.Bluray.x265.mkv',root.path||'/'||file.rel_path FROM movie_upgrade_organization_ingestion i JOIN library_file file ON file.id=i.file_id JOIN library_root root ON root.id=file.root_id WHERE i.operation_id=$1`, f.operation, f.client).Scan(&source, &target)
		if e != nil {
			t.Fatal(e)
		}
		src, e := os.Stat(source)
		if e != nil {
			t.Fatal(e)
		}
		dst, e := os.Stat(target)
		if e != nil || !os.SameFile(src, dst) {
			t.Fatal("not a real hardlink", e)
		}
		var n int
		if e = f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_organization_execution_permit WHERE operation_id=$1`, f.operation).Scan(&n); e != nil || n != 1 {
			t.Fatal("physical replay", n, e)
		}
		previewBody = fmt.Sprintf(`{"operation_id":%q}`, f.operation)
		retirementPreview := f.command("upgrade-retirement-previews", previewBody, "", f.admin, nil)
		if retirementPreview.Code != 200 {
			t.Fatal("retirement preview", retirementPreview.Code, retirementPreview.Body.String())
		}
		var retirementReview ports.MovieUpgradeRetirementPreview
		if e = json.Unmarshal(retirementPreview.Body.Bytes(), &retirementReview); e != nil {
			t.Fatal(e)
		}
		var retirementReceipt ports.MovieUpgradeRetirementReceipt
		command("upgrade-retirements", ports.ConfirmMovieUpgradeRetirement{OperationID: f.operation, InputStamp: retirementReview.InputStamp, Confirmed: true}, &retirementReceipt)
		waitPublicUpgradeObservation(t, func() bool {
			state, code := publicUpgradeOrganizationState(t, f, f.request.SubscriptionID)
			return code == 200 && state.Status == "retired" && state.NewFileID != nil && state.CurrentFileID == *state.NewFileID && state.RetainedUntil != nil && state.RetiredAt != nil
		})
		if info, e := os.Stat(target); e != nil || !os.SameFile(src, info) {
			t.Fatal("new file changed during retirement", e)
		}
		for endpoint, body := range bodies {
			got := f.command(endpoint, body, "org-app-"+endpoint, f.admin, nil)
			if got.Code != 202 || got.Body.String() != receipts[endpoint] {
				t.Fatal("post-retirement receipt", endpoint, got.Code, got.Body.String())
			}
		}
		if e = f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_retirement_permit WHERE operation_id=$1`, f.operation).Scan(&n); e != nil || n != 1 {
			t.Fatal("retirement physically replayed", n, e)
		}
	}
}
