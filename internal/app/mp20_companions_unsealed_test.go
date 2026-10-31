//go:build linux

package app

import (
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"os"
	"path/filepath"
	"testing"
)

func TestMP20CompanionsUnsealedRestartRequiresReviewPG(t *testing.T) {
	f, op, a, repo := mp20Parent(t, "hardlink")
	mp20Write(t, f, "Movie.2026.en.srt", mp20SRT)
	s := library.NewUntrackedCompanions(repo, rootfs.NewFilesystem())
	item, _ := mp20Select(t, s, a, op, "copy")
	if _, err := repo.ClaimCompanion(t.Context(), a, op, item.ID); err != nil {
		t.Fatal(err)
	}
	// The process vanished before it could persist content. Expiry is not a fresh
	// write opportunity: no receipt or postcondition can prove that attempt completed.
	f.exec(t, `UPDATE untracked_companion SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, item.ID)
	if _, err := s.Execute(t.Context(), a, op, item.ID); err == nil {
		t.Fatal("unsealed restart published a new target")
	}
	if _, err := os.Stat(filepath.Join(f.target, item.Candidate.TargetRelativePath)); !os.IsNotExist(err) {
		t.Fatal("unsealed restart created target", err)
	}
	view, err := s.Read(t.Context(), a, op)
	if err != nil || view.Items[0].State != "review_required" {
		t.Fatalf("unsealed restart did not remain manual %+v %v", view, err)
	}
	f.count(t, `SELECT count(*) FROM untracked_companion WHERE content IS NULL AND execution_token=2`, 1)
}
