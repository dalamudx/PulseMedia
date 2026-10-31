package postgres_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func absoluteCanonicalReviewFixture(t *testing.T) *untrackedReviewFixture {
	t.Helper()
	f, _ := canonicalReviewFixture(t)
	f.exec(t, `UPDATE media_unit SET absolute_number=103 WHERE id=$1`, uuid.MustParse(f.input.CanonicalUnitID))
	options, err := f.review.ReadUntrackedReviewEpisodeOptions(t.Context(), f.a, f.scope, f.file.ID, f.input.TMDBID, f.input.Season)
	if err != nil || len(options) != 1 {
		t.Fatal(options, err)
	}
	f.input.MediaMetadataVersion = options[0].MediaMetadataVersion
	f.input.UnitMetadataVersion = options[0].UnitMetadataVersion
	return f
}

func TestUntrackedReviewCanonicalAbsoluteOrganizationPG(t *testing.T) {
	f := absoluteCanonicalReviewFixture(t)
	org := organizationConfirm(t, f, canonicalReviewEvidence(), "hardlink")
	receipt := org.commit(t, "absolute-canonical")
	f.count(t, `SELECT count(*) FROM untracked_organization_unit WHERE operation_id=$1 AND unit_id=$2 AND unit_key='S01E07' AND tmdb_episode_id=1007 AND metadata_version=$3`, 1, receipt.Operation.ID, uuid.MustParse(f.input.CanonicalUnitID), f.input.UnitMetadataVersion)
	f.count(t, `SELECT count(*) FROM media_unit WHERE id=$1 AND absolute_number=103`, 1, uuid.MustParse(f.input.CanonicalUnitID))
	f.count(t, `SELECT count(*) FROM unit_fact`, 0)
	f.count(t, `SELECT count(*) FROM download_task`, 0)
}

func TestUntrackedReviewCanonicalAbsoluteOrganizationVersionChangePG(t *testing.T) {
	for _, phase := range []string{"prepare", "queue"} {
		t.Run(phase, func(t *testing.T) {
			f := absoluteCanonicalReviewFixture(t)
			org := organizationConfirm(t, f, canonicalReviewEvidence(), "hardlink")
			var capture ports.UntrackedOrganizationCapture
			if phase == "queue" {
				capture = org.prepare(t, "absolute-stale")
			}
			f.exec(t, `UPDATE media_unit SET absolute_number=104 WHERE id=$1`, uuid.MustParse(f.input.CanonicalUnitID))
			var err error
			if phase == "prepare" {
				_, err = org.organization.PrepareUntrackedOrganization(t.Context(), f.a, f.scope, f.file.ID, ports.UntrackedOrganizationInput{ConfirmationID: org.confirmation.Material.ID}, "absolute-stale")
			} else {
				_, err = org.organization.CommitUntrackedOrganization(t.Context(), capture)
			}
			checkinErr(t, err, ports.ErrUntrackedOrganizationStale)
			f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 0)
			f.count(t, `SELECT count(*) FROM transfer_plan`, 0)
		})
	}
}

func TestUntrackedReviewCanonicalAbsoluteAutomaticOrganizationRejectedPG(t *testing.T) {
	f := absoluteCanonicalReviewFixture(t)
	f.input = ports.UntrackedReviewInput{ScopeVersion: f.input.ScopeVersion, FileRevision: f.input.FileRevision, MediaType: "tv"}
	evidence := canonicalReviewEvidence()
	evidence.Selection = "automatic"
	org := organizationConfirm(t, f, evidence, "hardlink")
	_, err := org.organization.CommitUntrackedOrganization(t.Context(), org.prepare(t, "absolute-automatic"))
	checkinErr(t, err, ports.ErrUntrackedOrganizationStale)
	f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 0)
	f.count(t, `SELECT count(*) FROM transfer_plan`, 0)
}
