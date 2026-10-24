//go:build linux

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestM8OrganizationPublishedRecoveryAfterMinimumIncreaseRuntimeCookiePG(t *testing.T) {
	m8UntrackedOrganizationRequired(t)
	for _, action := range []string{"hardlink", "copy", "move"} {
		t.Run(action, func(t *testing.T) {
			f := m8UntrackedOrganizationNew(t, action)
			history := f.confirmationHistory(t)
			f.app.cfg.Organization.MinMediaBytes = f.sourceIdentity.Size
			receipt, raw := f.queue(t)
			release := f.failFirstFinalization(t, receipt)
			f.start(t)
			f.wait(t, "first publication rolled back before retry", func() bool {
				return f.predicate(t, `SELECT is_called FROM m8_untracked_organization_tail_sequence`) && f.predicate(t, `SELECT state='queued' AND attempts=1 FROM job WHERE id=$1`, receipt.Operation.JobID)
			})
			f.cancel()
			release()
			f.stop(t)
			for _, table := range []string{"untracked_organization_completion", "untracked_organization_observation", "library_file_unit"} {
				f.count(t, "SELECT count(*) FROM "+table, 0)
			}
			f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, 0)
			f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND attempts=1`, 1, receipt.Operation.JobID)
			f.count(t, `SELECT count(*) FROM untracked_organization_permit WHERE operation_id=$1 AND job_token=1`, 1, receipt.Operation.ID)
			targetBefore := m8UntrackedOrganizationIdentity(t, f.target, f.confirmation.Material.Input.TargetRelativePath)
			permitBefore := m8ConfigSnapshot(t, f.m7ScrapeFixture, "untracked_organization_permit", "transfer_content_evidence", "untracked_organization_operation", "untracked_organization_unit")
			frozenBefore := f.frozenTransfer(t, receipt)
			f.app.cfg.Organization.MinMediaBytes = f.sourceIdentity.Size + 1
			f.start(t)
			f.completed(t, receipt)
			f.stop(t)
			if permitBefore != m8ConfigSnapshot(t, f.m7ScrapeFixture, "untracked_organization_permit", "transfer_content_evidence", "untracked_organization_operation", "untracked_organization_unit") || frozenBefore != f.frozenTransfer(t, receipt) {
				t.Fatal("raised deployment minimum rewrote historical permission or content evidence")
			}
			if targetBefore != m8UntrackedOrganizationIdentity(t, f.target, f.confirmation.Material.Input.TargetRelativePath) {
				t.Fatal("observation-only recovery republished target")
			}
			f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='succeeded' AND attempts=2 AND lease_token=2`, 1, receipt.Operation.JobID)
			f.verifyImport(t, receipt, action)
			if action == "move" {
				if _, err := os.Lstat(filepath.Join(f.source, f.sourceRelative)); !os.IsNotExist(err) {
					t.Fatal("move recovery recreated source")
				}
			}
			f.recoverOffline(t, receipt, raw, "completed")
			if history != f.confirmationHistory(t) {
				t.Fatal("recovery changed original confirmation")
			}
			t.Logf("M8_MEDIA_SIZE_RECOVERY_PASS action=%s minimum_raised_after_publication=true restarted_App=true original_permit_token1=true observation_token2=true same_target_inode=true original_receipts=true downloader_calls=0", action)
		})
	}
}
