package postgres_test

import (
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func TestUntrackedOrganizationSparseMultiunitTVMetadataPG(t *testing.T) {
	f := newUntrackedReviewFixture(t, "tv")
	if _, e := f.discovery.RequestUntrackedDiscovery(t.Context(), f.scope, 1); e != nil {
		t.Fatal(e)
	}
	capture := f.claim(t)
	sample := []ports.UntrackedFileObservation{{RelativePath: "external/Series.S01E02E07.1080p.WEB-DL.mkv", Identity: ports.FileIdentity{Device: 2, Inode: 444, Size: 4096, Mtime: 200}}}
	if e := f.discovery.RecordUntrackedDiscovery(t.Context(), capture, sample); e != nil {
		t.Fatal(e)
	}
	if e := f.discovery.CompleteUntrackedDiscovery(t.Context(), capture); e != nil {
		t.Fatal(e)
	}
	for _, file := range f.files(t) {
		if file.RelativePath == sample[0].RelativePath {
			f.file = file
		}
	}
	if f.file.RelativePath != sample[0].RelativePath {
		t.Fatal("missing TV discovery")
	}
	f.input = ports.UntrackedReviewInput{ScopeVersion: 1, FileRevision: f.file.Revision, MediaType: "tv"}
	air := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	e := ports.UntrackedReviewEvidence{Selection: "automatic", Complete: true, Reason: "ready", ParsedTitle: "Series", Candidate: &ports.UntrackedReviewCandidate{Namespace: "tmdb.tv", Key: "tmdb.tv:123", TMDBID: 123, Title: "Series", Year: 2020, Units: []ports.UntrackedReviewUnit{{UnitKey: "S01E02", Season: 1, Episode: 2, TMDBEpisodeID: 1002, AirDate: &air}, {UnitKey: "S01E07", Season: 1, Episode: 7, TMDBEpisodeID: 1007}}}}
	org := organizationConfirm(t, f, e, "hardlink")
	before := organizationProtectedGraph(t, org)
	r := org.commit(t, "sparse")
	org.count(t, `SELECT count(*) FROM transfer_step WHERE plan_id=$1`, 1, r.Operation.PlanID)
	org.count(t, `SELECT count(*) FROM untracked_organization_unit WHERE operation_id=$1`, 2, r.Operation.ID)
	org.count(t, `SELECT count(*) FROM media_unit WHERE unit_key IN ('S01E02','S01E07')`, 2)
	org.count(t, `SELECT count(*) FROM media_unit WHERE unit_key IN ('S01E01','S01E03','S01E04','S01E05','S01E06')`, 0)
	org.count(t, `SELECT count(*) FROM media_unit_identity i JOIN media_unit u ON u.id=i.unit_id WHERE i.namespace='tmdb.episode' AND i.tmdb_series_id=123 AND ((u.unit_key='S01E02' AND i.external_id=1002) OR (u.unit_key='S01E07' AND i.external_id=1007))`, 2)
	org.count(t, `SELECT count(*) FROM untracked_organization_unit f JOIN media_unit u ON u.id=f.unit_id JOIN media m ON m.id=f.media_id WHERE f.operation_id=$1 AND f.metadata_version=u.metadata_version AND f.media_metadata_version=m.metadata_version`, 2, r.Operation.ID)
	org.count(t, `SELECT count(*) FROM media_unit WHERE unit_key='S01E07' AND air_date IS NULL`, 1)
	org.count(t, `SELECT count(*) FROM transfer_step s JOIN media_unit u ON u.id=s.unit_id WHERE s.id=$1 AND u.unit_key='S01E02' AND NOT (s.target_evidence ?| ARRAY['episode_operation_id','automatic_tv_operation_id','complete_tv_operation_id','file_action'])`, 1, r.Operation.StepID)
	if after := organizationProtectedGraph(t, org); after != before {
		t.Fatal("sparse publication wrote unrelated graph")
	}
	job, err := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.UntrackedOrganizationJobKind}, Column2: "tv-organization", Column3: 120000})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := org.organization.LoadUntrackedOrganizationExecution(t.Context(), ports.JobLease{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken})
	if err != nil || len(frame.Units) != 2 {
		t.Fatal(frame, err)
	}
	f.exec(t, `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE unit_key='S01E07'`)
	if _, err = org.organization.LoadUntrackedOrganizationExecution(t.Context(), ports.JobLease{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken}); err == nil {
		t.Fatal("changed secondary TV unit accepted")
	}
}
