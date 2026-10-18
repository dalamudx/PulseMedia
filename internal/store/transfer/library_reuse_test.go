package transfer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func reusePreviewFixture(t *testing.T, unknown ...bool) (*safetyFixture, *Repository, *ports.Actor, ports.LibraryReuseTarget, uuid.UUID) {
	t.Helper()
	f := safetySetup(t)
	who := uuid.New()
	if _, err := f.q.CreateUser(t.Context(), db.CreateUserParams{ID: who, Username: "reuse-admin", PasswordHash: "unused-password-hash", Role: "admin", IsActive: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE library_root SET baseline_completed=true WHERE id=$1", f.root); err != nil {
		t.Fatal(err)
	}
	profile := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion}
	if len(unknown) > 0 && unknown[0] {
		profile.SourceParserVersion = ""
	}
	plan, step, err := f.svc.CreateMoviePlan(t.Context(), uuid.New(), f.root, f.unit, f.sourcePath, "Movie", 2020, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.ExecutePlan(t.Context(), plan.ID); err != nil {
		t.Fatal(err)
	}
	source, err := f.q.GetActiveFileByPath(t.Context(), db.GetActiveFileByPathParams{RootID: f.root, RelPath: step.TargetRel.String})
	if err != nil {
		t.Fatal(err)
	}
	target := uuid.New()
	path := t.TempDir()
	if err = os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("reuse target"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.q.CreateLibraryRoot(t.Context(), db.CreateLibraryRootParams{ID: target, Path: path, MediaType: "movie", Name: "Reuse destination"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), "UPDATE library_root SET baseline_completed=true WHERE id=$1", target); err != nil {
		t.Fatal(err)
	}
	policy, err := f.q.GetLibraryReusePolicy(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	return f, &Repository{execution: f.svc}, &ports.Actor{Type: ports.ActorTypeUser, ID: who.String(), Role: ports.RoleAdmin}, ports.LibraryReuseTarget{UnitID: f.unit, RootID: target, PolicyID: "default", PolicyRevision: policy.CurrentRevision}, source.ID
}

func reusePreview(t *testing.T, r *Repository, a *ports.Actor, target ports.LibraryReuseTarget) ports.LibraryReusePreview {
	t.Helper()
	result, err := r.PreviewLibraryReuseBatch(t.Context(), a, []ports.LibraryReuseTarget{target})
	if err != nil || len(result) != 1 {
		t.Fatalf("reuse preview: %+v %v", result, err)
	}
	return result[0]
}

func TestLibraryReusePreviewTargetIsolationPG(t *testing.T) {
	f, r, a, target, file := reusePreviewFixture(t)
	counts := map[string]int{}
	for _, table := range []string{"transfer_plan", "transfer_step", "job", "acquisition_reservation", "library_file", "unit_fact", "outbox_event"} {
		counts[table] = f.count(t, table)
	}
	sourceTarget := target
	sourceTarget.RootID = f.root
	batch, err := r.PreviewLibraryReuseBatch(t.Context(), a, []ports.LibraryReuseTarget{target, sourceTarget})
	if err != nil || len(batch) != 2 {
		t.Fatal(batch, err)
	}
	candidate := batch[0]
	if candidate.AlreadyOwned || !candidate.CanConfirm || !candidate.Complete || len(candidate.Candidates) != 1 || candidate.Candidates[0].FileID != file || !candidate.Candidates[0].Qualified || !candidate.Candidates[0].SourceAvailable {
		t.Fatalf("other root is only a source: %+v", candidate)
	}
	if !batch[1].AlreadyOwned || batch[1].CanConfirm || batch[1].WaitReason != "already_owned" {
		t.Fatalf("target-root ownership: %+v", batch[1])
	}
	if next := reusePreview(t, r, a, target); next.InputStamp != candidate.InputStamp {
		t.Fatalf("unchanged inputs changed material: %s %s", candidate.InputStamp, next.InputStamp)
	}
	for table, before := range counts {
		if got := f.count(t, table); got != before {
			t.Fatalf("preview mutated %s: %d -> %d", table, before, got)
		}
	}
}

func TestLibraryReusePreviewSourceChangesPG(t *testing.T) {
	f, r, a, target, file := reusePreviewFixture(t)
	before := reusePreview(t, r, a, target)
	if _, err := f.pool.Exec(t.Context(), "UPDATE library_root SET health='offline',health_revision=health_revision+1 WHERE id=$1", f.root); err != nil {
		t.Fatal(err)
	}
	offline := reusePreview(t, r, a, target)
	if offline.CanConfirm || offline.AlreadyOwned || len(offline.Candidates) != 1 || !offline.Candidates[0].Qualified || offline.Candidates[0].SourceAvailable || offline.Candidates[0].WaitReason != "source_unavailable" || offline.InputStamp == before.InputStamp {
		t.Fatalf("offline source must wait without ownership: %+v", offline)
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE library_root SET health='online',health_revision=health_revision+1 WHERE id=$1", f.root); err != nil {
		t.Fatal(err)
	}
	restored := reusePreview(t, r, a, target)
	if !restored.CanConfirm {
		t.Fatal("healthy source did not become selectable", restored)
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE library_file_unit SET retired_at=clock_timestamp() WHERE file_id=$1", file); err != nil {
		t.Fatal(err)
	}
	corrected := reusePreview(t, r, a, target)
	if corrected.CanConfirm || corrected.AlreadyOwned || len(corrected.Candidates) != 0 || corrected.InputStamp == restored.InputStamp {
		t.Fatalf("retired mapping reused historical fact: %+v", corrected)
	}
}

func TestLibraryReusePreviewCurrentActorPG(t *testing.T) {
	f, r, a, target, _ := reusePreviewFixture(t)
	if _, err := r.PreviewLibraryReuseBatch(t.Context(), nil, []ports.LibraryReuseTarget{target}); !errors.Is(err, ports.ErrUnauthorized) {
		t.Fatal("anonymous", err)
	}
	token := *a
	token.Scopes = []string{"read"}
	if _, err := r.PreviewLibraryReuseBatch(t.Context(), &token, []ports.LibraryReuseTarget{target}); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("token", err)
	}
	if _, err := r.PreviewLibraryReuseBatch(t.Context(), a, []ports.LibraryReuseTarget{target, target}); !errors.Is(err, ports.ErrInvalidLibraryReuse) {
		t.Fatal("duplicate target", err)
	}
	stale := target
	stale.PolicyRevision++
	if _, err := r.PreviewLibraryReuseBatch(t.Context(), a, []ports.LibraryReuseTarget{stale}); !errors.Is(err, ports.ErrLibraryReuseChanged) {
		t.Fatal("stale policy", err)
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(a.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PreviewLibraryReuseBatch(t.Context(), a, []ports.LibraryReuseTarget{target}); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revoked account", err)
	}
}

func TestLibraryReusePreviewUnknownQualityPG(t *testing.T) {
	f, r, a, target, _ := reusePreviewFixture(t, true)
	preview := reusePreview(t, r, a, target)
	if preview.CanConfirm || preview.AlreadyOwned || len(preview.Candidates) != 1 || preview.Candidates[0].Qualified || preview.Candidates[0].WaitReason != "ownership_unknown" {
		t.Fatalf("unknown provenance permitted reuse: %+v", preview)
	}
	target.RootID = f.root
	owned := reusePreview(t, r, a, target)
	if owned.AlreadyOwned || !owned.OwnershipUnknown || owned.CanConfirm {
		t.Fatalf("unknown provenance certified ownership: %+v", owned)
	}
}
