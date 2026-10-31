//go:build linux

package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMP20CompanionsPermissionsRevocationAndConcurrentSelectionPG(t *testing.T) {
	f, op, a, repo := mp20Parent(t, "hardlink")
	mp20Write(t, f, "Movie.2026.en.srt", mp20SRT)
	s := library.NewUntrackedCompanions(repo, rootfs.NewFilesystem())
	candidates, err := s.Discover(t.Context(), a, op)
	if err != nil || len(candidates) != 1 {
		t.Fatal("discovery", err)
	}
	in := ports.UntrackedCompanionSelection{CandidateKeys: []string{candidates[0].Key}, Action: "copy"}
	var wg sync.WaitGroup
	results := make(chan *ports.CompanionView, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() { view, e := s.Select(t.Context(), a, op, "concurrent", in); results <- view; failures <- e })
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal("same-key concurrent selection", err)
		}
	}
	var id uuid.UUID
	for view := range results {
		if view == nil || len(view.Items) != 1 {
			t.Fatal("duplicated selection", view)
		}
		if id != uuid.Nil && id != view.Items[0].ID {
			t.Fatal("changed attachment identity")
		}
		id = view.Items[0].ID
	}
	f.count(t, `SELECT count(*) FROM untracked_companion`, 1)
	f.count(t, `SELECT count(*) FROM audit_log WHERE action='untracked.companions.selected'`, 1)
	bad := in
	bad.Action = "hardlink"
	if _, err = s.Select(t.Context(), a, op, "concurrent", bad); err == nil {
		t.Fatal("same key changed action")
	}
	for _, actor := range []*ports.Actor{nil, {ID: a.ID, Type: ports.ActorTypeUser, Role: ports.RoleMember}, {ID: a.ID, Type: ports.ActorTypeUser, Role: ports.RoleAdmin, Scopes: []string{}}, {ID: uuid.NewString(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}} {
		if _, err = s.Execute(t.Context(), actor, op, id); err == nil {
			t.Fatal("invalid current subject authorized publish")
		}
	}
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, f.admin)
	if _, err = s.Execute(t.Context(), a, op, id); err == nil {
		t.Fatal("stale admin actor bypassed current revocation")
	}
	f.count(t, `SELECT count(*) FROM untracked_companion WHERE content IS NOT NULL`, 0)
	f.exec(t, `UPDATE app_user SET is_active=true WHERE id=$1`, f.admin)
	staging := filepath.Join(f.target, ".pulsemedia_transfers")
	if err = os.Mkdir(staging, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(staging, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(t.Context(), a, op, id); err == nil {
		t.Fatal("unsafe staging permissions accepted")
	}
	if _, err = os.Stat(filepath.Join(f.target, candidates[0].TargetRelativePath)); !os.IsNotExist(err) {
		t.Fatal("unsafe stage published subtitle", err)
	}
	if err = os.Chmod(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(t.Context(), a, op, id); err == nil {
		t.Fatal("permanent permit was rewritten after permission repair")
	}
	f.count(t, `SELECT count(*) FROM untracked_companion WHERE content IS NOT NULL AND result IS NULL AND state='unknown'`, 1)
	if body, err := os.ReadFile(filepath.Join(f.source, candidates[0].SourceRelativePath)); err != nil || string(body) != mp20SRT {
		t.Fatal("failed copy did not retain source")
	}
}

type mp20CountingFilesystem struct {
	*rootfs.Filesystem
	sends atomic.Int32
}

func (f *mp20CountingFilesystem) TransferVerified(ctx context.Context, src ports.TransferRoot, rel string, dst ports.TransferRoot, target string, p ports.TransferContent, action string, id uuid.UUID) (ports.VerifiedTransferResult, error) {
	f.sends.Add(1)
	return f.Filesystem.TransferVerified(ctx, src, rel, dst, target, p, action, id)
}
func TestMP20CompanionsFinalizeRollbackRecoversByObservationPG(t *testing.T) {
	f, op, a, repo := mp20Parent(t, "hardlink")
	mp20Write(t, f, "Movie.2026.zh.srt", mp20SRT)
	files := &mp20CountingFilesystem{Filesystem: rootfs.NewFilesystem()}
	s := library.NewUntrackedCompanions(repo, files)
	item, _ := mp20Select(t, s, a, op, "copy")
	f.exec(t, `CREATE FUNCTION mp20_reject_completion() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='completed' THEN RAISE EXCEPTION 'owned completion rollback'; END IF; RETURN NEW; END $$`)
	f.exec(t, `CREATE CONSTRAINT TRIGGER mp20_completion_failure AFTER UPDATE ON untracked_companion DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mp20_reject_completion()`)
	if _, err := s.Execute(t.Context(), a, op, item.ID); err == nil {
		t.Fatal("injected transaction failure ignored")
	}
	if files.sends.Load() != 1 {
		t.Fatal("copy not performed before tail rollback")
	}
	original := m8UntrackedOrganizationIdentity(t, f.target, item.Candidate.TargetRelativePath)
	f.count(t, `SELECT count(*) FROM untracked_companion WHERE result IS NULL AND content IS NOT NULL AND state='unknown'`, 1)
	f.count(t, `SELECT count(*) FROM audit_log WHERE action='untracked.companion.completed'`, 0)
	f.exec(t, `DROP TRIGGER mp20_completion_failure ON untracked_companion`)
	result, err := s.Execute(t.Context(), a, op, item.ID)
	if err != nil || result.State != "completed" || files.sends.Load() != 1 {
		t.Fatalf("read-only tail recovery %+v sends=%d %v", result, files.sends.Load(), err)
	}
	if original != m8UntrackedOrganizationIdentity(t, f.target, item.Candidate.TargetRelativePath) {
		t.Fatal("recovery republished target")
	}
	f.count(t, `SELECT count(*) FROM untracked_companion WHERE permit_token=1 AND execution_token=2`, 1)
	f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='untracked.companion.completed'`, 1)
}
