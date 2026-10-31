//go:build linux

package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
)

const mp20SRT = "1\n00:00:00,000 --> 00:00:02,000\nOwned companion subtitle\n"

func mp20Parent(t *testing.T, action string) (*m8UntrackedOrganizationFixture, uuid.UUID, *ports.Actor, *postgres.UntrackedCompanionStore) {
	t.Helper()
	m8UntrackedOrganizationRequired(t)
	f := m8UntrackedOrganizationNew(t, action)
	receipt, _ := f.queue(t)
	f.start(t)
	f.completed(t, receipt)
	f.stop(t)
	a := &ports.Actor{ID: f.admin.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	repo := postgres.NewUntrackedCompanionStore(f.pool, f.app.IDGen(), f.app.Clock())
	return f, receipt.Operation.ID, a, repo
}
func mp20Write(t *testing.T, f *m8UntrackedOrganizationFixture, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.source, name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func mp20Select(t *testing.T, s *library.UntrackedCompanions, a *ports.Actor, op uuid.UUID, action string) (ports.CompanionAttachment, ports.UntrackedCompanionSelection) {
	t.Helper()
	items, err := s.Discover(t.Context(), a, op)
	if err != nil || len(items) != 1 {
		t.Fatalf("native companion discovery: %+v %v", items, err)
	}
	in := ports.UntrackedCompanionSelection{CandidateKeys: []string{items[0].Key}, Action: action}
	view, err := s.Select(t.Context(), a, op, "companion-selection", in)
	if err != nil || len(view.Items) != 1 || view.Items[0].State != "pending" {
		t.Fatalf("frozen selection %+v %v", view, err)
	}
	return view.Items[0], in
}
func TestMP20CompanionsCopyHardlinkAndMovedVideoPG(t *testing.T) {
	for _, mode := range []struct{ video, attachment string }{{"hardlink", "hardlink"}, {"copy", "copy"}, {"move", "copy"}} {
		t.Run(mode.video+"_"+mode.attachment, func(t *testing.T) {
			f, op, a, repo := mp20Parent(t, mode.video)
			mp20Write(t, f, "Movie.2026.zh-Hans.srt", mp20SRT)
			mp20Write(t, f, "Other.ass", "unrelated")
			s := library.NewUntrackedCompanions(repo, rootfs.NewFilesystem())
			item, in := mp20Select(t, s, a, op, mode.attachment)
			if item.Candidate.TargetRelativePath != "Movie (2026)/Movie.zh-Hans.srt" {
				t.Fatalf("playable basename %q", item.Candidate.TargetRelativePath)
			}
			result, err := s.Execute(t.Context(), a, op, item.ID)
			if err != nil || result.State != "completed" || !result.SourceRetained {
				t.Fatalf("companion completion: %+v %v", result, err)
			}
			source, err := os.ReadFile(filepath.Join(f.source, item.Candidate.SourceRelativePath))
			if err != nil || string(source) != mp20SRT {
				t.Fatal("source subtitle removed or changed", err)
			}
			target, err := os.ReadFile(filepath.Join(f.target, item.Candidate.TargetRelativePath))
			if err != nil || !bytes.Equal(source, target) {
				t.Fatal("target subtitle content differs", err)
			}
			st, err := os.Stat(filepath.Join(f.target, item.Candidate.TargetRelativePath))
			if err != nil || st.Mode().Perm() != 0600 {
				t.Fatal("subtitle permissions", err)
			}
			original := m8UntrackedOrganizationIdentity(t, f.target, item.Candidate.TargetRelativePath)
			if mode.attachment == "hardlink" && original.Inode != item.Candidate.Identity.Inode {
				t.Fatal("hardlink not same inode")
			}
			// A fresh instance after restart reads stored completion and never rewrites.
			restarted := library.NewUntrackedCompanions(postgres.NewUntrackedCompanionStore(f.pool, f.app.IDGen(), f.app.Clock()), rootfs.NewFilesystem())
			if _, err = restarted.Execute(t.Context(), a, op, item.ID); err != nil {
				t.Fatal("restart completion", err)
			}
			if original != m8UntrackedOrganizationIdentity(t, f.target, item.Candidate.TargetRelativePath) {
				t.Fatal("restart rewrote subtitle")
			}
			if err = os.Rename(f.source, f.source+"-offline"); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Rename(f.source+"-offline", f.source) }()
			if _, err = restarted.Select(t.Context(), a, op, "companion-selection", in); err != nil {
				t.Fatal("original selection reread touched offline source", err)
			}
			view, err := restarted.Read(t.Context(), a, op)
			if err != nil || view.VideoState != "completed" || view.Items[0].State != "completed" {
				t.Fatalf("independent outcomes %+v %v", view, err)
			}
			f.count(t, `SELECT count(*) FROM untracked_companion WHERE content IS NOT NULL AND permit_owner IS NOT NULL AND permit_token=1 AND result IS NOT NULL`, 1)
			f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, 1)
			f.count(t, `SELECT count(*) FROM audit_log WHERE action='untracked.companion.completed'`, 1)
			// Immutable intent, permanent permit, terminal outcome and destructive downgrade are guarded.
			for _, sql := range []string{`UPDATE untracked_companion SET candidate='{}'`, `UPDATE untracked_companion SET content=NULL,permit_owner=NULL,permit_token=NULL,permit_issued_at=NULL`, `DELETE FROM untracked_companion`, `TRUNCATE untracked_companion`} {
				if _, err = f.pool.Exec(t.Context(), sql); err == nil {
					t.Fatal("companion history mutation accepted", sql)
				}
			}
		})
	}
}
func TestMP20CompanionsNoOverwriteIdentityAndPartialOutcomesPG(t *testing.T) {
	f, op, a, repo := mp20Parent(t, "hardlink")
	mp20Write(t, f, "Movie.2026.en.ass", "[Script Info]\nTitle: Owned\n")
	mp20Write(t, f, "Movie.2026.zh.srt", mp20SRT)
	s := library.NewUntrackedCompanions(repo, rootfs.NewFilesystem())
	candidates, err := s.Discover(t.Context(), a, op)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("two candidates %v %v", candidates, err)
	}
	keys := []string{candidates[0].Key, candidates[1].Key}
	view, err := s.Select(t.Context(), a, op, "both", ports.UntrackedCompanionSelection{CandidateKeys: keys, Action: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	var conflict, good ports.CompanionAttachment
	for _, item := range view.Items {
		if filepath.Ext(item.Candidate.TargetRelativePath) == ".ass" {
			conflict = item
		} else {
			good = item
		}
	}
	protected := filepath.Join(f.target, conflict.Candidate.TargetRelativePath)
	if err = os.WriteFile(protected, []byte("user existing subtitle"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(t.Context(), a, op, conflict.ID); err == nil {
		t.Fatal("existing target accepted")
	}
	if body, _ := os.ReadFile(protected); string(body) != "user existing subtitle" {
		t.Fatal("existing subtitle overwritten")
	}
	if _, err = s.Execute(t.Context(), a, op, good.ID); err != nil {
		t.Fatal("unrelated attachment failed", err)
	}
	view, err = s.Read(t.Context(), a, op)
	if err != nil || view.VideoState != "completed" {
		t.Fatal("video outcome lost", err)
	}
	completed := 0
	for _, item := range view.Items {
		if item.State == "completed" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("partial result hidden %+v", view)
	}
	// Replacing the frozen source cannot authorize a fresh physical attempt.
	if err = os.Remove(filepath.Join(f.source, conflict.Candidate.SourceRelativePath)); err != nil {
		t.Fatal(err)
	}
	mp20Write(t, f, conflict.Candidate.SourceRelativePath, "replacement")
	if err = os.Remove(protected); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(t.Context(), a, op, conflict.ID); err == nil {
		t.Fatal("replaced source identity accepted")
	}
	if _, err = os.Stat(protected); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replacement was published")
	}
	f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, 1)
}

type mp20HeldFilesystem struct {
	*rootfs.Filesystem
	entered chan struct{}
	release chan struct{}
	sends   atomic.Int32
}

func (f *mp20HeldFilesystem) TransferVerified(ctx context.Context, source ports.TransferRoot, rel string, target ports.TransferRoot, dest string, proof ports.TransferContent, action string, id uuid.UUID) (ports.VerifiedTransferResult, error) {
	f.sends.Add(1)
	result, err := f.Filesystem.TransferVerified(ctx, source, rel, target, dest, proof, action, id)
	close(f.entered)
	<-f.release
	return result, err
}
func TestMP20CompanionsCancelLateReturnPhysicalLockAndRestartPG(t *testing.T) {
	f, op, a, repo := mp20Parent(t, "hardlink")
	mp20Write(t, f, "Movie.2026.zh.srt", mp20SRT)
	files := &mp20HeldFilesystem{Filesystem: rootfs.NewFilesystem(), entered: make(chan struct{}), release: make(chan struct{})}
	s := library.NewUntrackedCompanions(repo, files)
	item, _ := mp20Select(t, s, a, op, "copy")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { defer close(done); _, err := s.Execute(ctx, a, op, item.ID); done <- err }()
	select {
	case <-files.entered:
	case <-time.After(10 * time.Second):
		close(files.release)
		t.Fatal("native transfer did not enter")
	}
	released := false
	defer func() {
		if !released {
			close(files.release)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}()
	cancel()
	f.exec(t, `UPDATE untracked_companion SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, item.ID)
	// Expired logical lease cannot free the still-running native root locks.
	competing, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, err := s.Execute(competing, a, op, item.ID)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) || files.sends.Load() != 1 {
		t.Fatalf("physical overlap/rewrite: %d %v", files.sends.Load(), err)
	}
	select {
	case err = <-done:
		t.Fatal("cancel released physical call early", err)
	default:
	}
	close(files.release)
	released = true
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("late return committed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("physical return not joined")
	}
	view, err := s.Read(t.Context(), a, op)
	if err != nil || view.Items[0].State != "unknown" {
		t.Fatalf("expired result was not unknown %+v %v", view, err)
	}
	// New instance observes the original content permit; its publishing wrapper would
	// close entered twice if a recovery ever called TransferVerified again.
	restarted := library.NewUntrackedCompanions(postgres.NewUntrackedCompanionStore(f.pool, f.app.IDGen(), f.app.Clock()), files)
	result, err := restarted.Execute(t.Context(), a, op, item.ID)
	if err != nil || result.State != "completed" || files.sends.Load() != 1 {
		t.Fatalf("read-only recovery %+v %d %v", result, files.sends.Load(), err)
	}
	f.count(t, `SELECT count(*) FROM untracked_companion WHERE permit_token=1 AND execution_token=2`, 1)
}
func TestMP20CompanionsPermitBeforeWriteFailureDoesNotReplayPG(t *testing.T) {
	f, op, a, repo := mp20Parent(t, "hardlink")
	mp20Write(t, f, "Movie.2026.en.srt", mp20SRT)
	s := library.NewUntrackedCompanions(repo, rootfs.NewFilesystem())
	item, _ := mp20Select(t, s, a, op, "copy")
	// Crash boundary after durable permit, before any filesystem publication.
	e, err := repo.ClaimCompanion(t.Context(), a, op, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	fs := rootfs.NewFilesystem()
	roots, release, err := fs.LockRoots(t.Context(), f.source, f.target)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := fs.InspectContent(t.Context(), roots[f.source], item.Candidate.SourceRelativePath, item.Candidate.Identity)
	release()
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.SealCompanion(t.Context(), a, *e, proof); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE untracked_companion SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, item.ID)
	if _, err = s.Execute(t.Context(), a, op, item.ID); err == nil {
		t.Fatal("missing postcondition accepted")
	}
	if _, err = os.Stat(filepath.Join(f.target, item.Candidate.TargetRelativePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery blindly published missing target")
	}
	view, err := s.Read(t.Context(), a, op)
	if err != nil || view.Items[0].State != "unknown" {
		t.Fatal("unknown permit was reset", err)
	}
	f.count(t, `SELECT count(*) FROM untracked_companion WHERE content IS NOT NULL AND permit_token=1 AND result IS NULL`, 1)
}
