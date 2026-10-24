//go:build linux

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestM8OrganizationMeasuredSizeBoundaryRuntimeCookiePG(t *testing.T) {
	m8UntrackedOrganizationRequired(t)
	for _, mode := range []string{"at_threshold", "below_threshold"} {
		t.Run(mode, func(t *testing.T) {
			f := m8UntrackedOrganizationNew(t, "hardlink")
			history := f.confirmationHistory(t)
			minimum := f.sourceIdentity.Size
			if mode == "below_threshold" {
				minimum++
			}
			f.app.cfg.Organization.MinMediaBytes = minimum
			receipt, raw := f.queue(t)
			sourceBefore, targetBefore := m8UntrackedOrganizationTree(t, f.source), m8UntrackedOrganizationTree(t, f.target)
			f.start(t)
			if mode == "at_threshold" {
				f.completed(t, receipt)
				f.stop(t)
				f.verifyImport(t, receipt, "hardlink")
				f.count(t, `SELECT count(*) FROM untracked_organization_permit WHERE operation_id=$1`, 1, receipt.Operation.ID)
				// A later deployment rule cannot invalidate the historical original receipt.
				f.app.cfg.Organization.MinMediaBytes = minimum + 1
				f.recoverOffline(t, receipt, raw, "completed")
			} else {
				f.wait(t, "below-threshold work rejected before permanent permission", func() bool {
					return f.predicate(t, `SELECT state='dead' AND attempts=3 FROM job WHERE id=$1`, receipt.Operation.JobID)
				})
				f.stop(t)
				for _, table := range []string{"untracked_organization_permit", "transfer_content_evidence", "untracked_organization_completion", "untracked_organization_observation", "library_file_unit"} {
					f.count(t, "SELECT count(*) FROM "+table, 0)
				}
				f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, 0)
				f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='media.imported'`, 0)
				if sourceBefore != m8UntrackedOrganizationTree(t, f.source) || targetBefore != m8UntrackedOrganizationTree(t, f.target) {
					t.Fatal("excluded small file changed physical source or target")
				}
				if _, err := os.Lstat(filepath.Join(f.target, f.confirmation.Material.Input.TargetRelativePath)); !os.IsNotExist(err) {
					t.Fatal("excluded small file published target")
				}
				f.recoverOffline(t, receipt, raw, "failed")
			}
			if history != f.confirmationHistory(t) || f.protected != f.protectedGraph(t) || f.calls.Load() != 0 {
				t.Fatal("size filtering changed confirmation, acquisition graph or downloader calls")
			}
			t.Logf("M8_MEDIA_SIZE_BOUNDARY_PASS scenario=%s real_App=true real_Cookie=true real_PG=true original_receipt=true downloader_calls=0", mode)
		})
	}
}
