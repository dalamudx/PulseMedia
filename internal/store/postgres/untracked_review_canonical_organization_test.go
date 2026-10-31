package postgres_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestUntrackedReviewCanonicalOrganizationRetainsConfirmedVersionsPG(t *testing.T) {
	for _, phase := range []string{"prepare", "queue"} {
		for _, which := range []string{"media", "unit"} {
			t.Run(phase+"_"+which, func(t *testing.T) {
				f, mid := canonicalReviewFixture(t)
				org := organizationConfirm(t, f, canonicalReviewEvidence(), "hardlink")
				var capture ports.UntrackedOrganizationCapture
				if phase == "queue" {
					capture = org.prepare(t, "canonical-organization")
				}
				if which == "media" {
					f.exec(t, `UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1`, mid)
				} else {
					f.exec(t, `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1`, uuid.MustParse(f.input.CanonicalUnitID))
				}
				var err error
				if phase == "prepare" {
					_, err = org.organization.PrepareUntrackedOrganization(t.Context(), f.a, f.scope, f.file.ID, ports.UntrackedOrganizationInput{ConfirmationID: org.confirmation.Material.ID}, "canonical-organization")
				} else {
					_, err = org.organization.CommitUntrackedOrganization(t.Context(), capture)
				}
				checkinErr(t, err, ports.ErrUntrackedOrganizationStale)
				f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 0)
				f.count(t, `SELECT count(*) FROM transfer_plan`, 0)
				f.count(t, `SELECT count(*) FROM unit_fact`, 0)
			})
		}
	}
}

func TestUntrackedReviewCanonicalOrganizationHistoricalReceiptPG(t *testing.T) {
	f, mid := canonicalReviewFixture(t)
	org := organizationConfirm(t, f, canonicalReviewEvidence(), "hardlink")
	receipt := org.commit(t, "canonical-organization")
	f.exec(t, `UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1`, mid)
	recovered, err := org.organization.PrepareUntrackedOrganization(t.Context(), f.a, f.scope, f.file.ID, ports.UntrackedOrganizationInput{ConfirmationID: org.confirmation.Material.ID}, "canonical-organization")
	if err != nil || recovered == nil || recovered.Receipt == nil || recovered.Capture != nil || !sameHistoricalJSON(receipt, recovered.Receipt) {
		t.Fatal("historical queued receipt changed", recovered, err)
	}
	f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 1)
	f.count(t, `SELECT count(*) FROM unit_fact`, 0)
}

func TestUntrackedReviewCanonicalOrganizationTailRollbackPG(t *testing.T) {
	f, mid := canonicalReviewFixture(t)
	org := organizationConfirm(t, f, canonicalReviewEvidence(), "hardlink")
	capture := org.prepare(t, "canonical-tail")
	f.exec(t, `CREATE FUNCTION mp19_change_metadata_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE media SET metadata_version=metadata_version+1 WHERE id='`+mid.String()+`'::uuid; RETURN NULL; END $$;
 CREATE CONSTRAINT TRIGGER mp19_change_metadata_tail AFTER INSERT ON untracked_organization_operation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mp19_change_metadata_tail()`)
	_, err := org.organization.CommitUntrackedOrganization(t.Context(), capture)
	// Deferred SQL graph guards reject the changed versions before the application recheck.
	checkinErr(t, err, ports.ErrUnavailable)
	f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 0)
	f.count(t, `SELECT count(*) FROM transfer_plan`, 0)
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_organization'`, 0)
	f.count(t, `SELECT count(*) FROM unit_fact`, 0)
	f.count(t, `SELECT count(*) FROM media WHERE id=$1 AND metadata_version=$2`, 1, mid, f.input.MediaMetadataVersion)
}
