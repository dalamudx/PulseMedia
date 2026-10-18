package httpapi_test

import (
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestUpgradePreviewPGVersionFence(t *testing.T) {
	for _, change := range []string{"policy", "file", "fact", "candidate", "source", "projection"} {
		t.Run(change, func(t *testing.T) {
			f := newUpgradeFixture(t)
			v := upgradeResult(t, f.call(f.body(), f.admin, nil))
			f.request.Expected = &v.Version
			switch change {
			case "policy":
				f.exec(t, "INSERT INTO policy_revision(id,policy_id,revision,rules) SELECT $1,policy_id,2,rules FROM policy_revision WHERE policy_id='upgrade-test' AND revision=1", uuid.New())
				f.exec(t, "UPDATE policy SET current_revision=2 WHERE id='upgrade-test'")
			case "file":
				f.exec(t, "UPDATE library_file SET size_bytes=size_bytes+1 WHERE id=$1", f.request.CurrentFileID)
			case "fact":
				f.exec(t, "UPDATE unit_fact SET quality_profile=quality_profile || '{\"codec_parser_version\":0}'::jsonb WHERE id=$1", f.fact)
			case "candidate":
				f.setCandidate(t, f.request.CandidateID, "Fight.Club.1999.1080p.Bluray.x264")
			case "source":
				f.exec(t, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", f.source)
			case "projection":
				f.exec(t, "UPDATE wanted_item SET next_evaluate_at=clock_timestamp() WHERE subscription_id=$1", f.request.SubscriptionID)
			}
			before := f.state(t)
			w := f.call(f.body(), f.admin, nil)
			if w.Code != 409 {
				t.Fatalf("stale %s accepted: %d %s", change, w.Code, w.Body.String())
			}
			if before != f.state(t) {
				t.Fatal("fence rejection wrote state")
			}
		})
	}
}
func TestUpgradePreviewPGUnsupportedMultipleUnits(t *testing.T) {
	f := newUpgradeFixture(t)
	f.exec(t, "INSERT INTO media_unit(id,media_id,unit_key) SELECT $1,media_id,'S01E02' FROM media_unit WHERE id=$2", uuid.New(), f.unit)
	before := f.state(t)
	p := upgradeResult(t, f.call(f.body(), f.admin, nil))
	if p.Comparable || p.Improvement || !strings.Contains(p.Reason, "standard_single_movie_required") || p.Version.InputStamp == "" || p.ObservedAt.IsZero() {
		t.Fatal(p)
	}
	f.request.Expected = &p.Version
	p2 := upgradeResult(t, f.call(f.body(), f.admin, nil))
	if p.Version != p2.Version {
		t.Fatal("unsupported scope fence unstable")
	}
	if before != f.state(t) {
		t.Fatal("unsupported scope wrote state")
	}
}
func TestUpgradePreviewPGCurrentPolicyAllowedAttributes(t *testing.T) {
	f := newUpgradeFixture(t)
	f.setCandidate(t, f.request.CandidateID, "Fight.Club.1999.2160p.WEB-DL.x265")
	before := f.state(t)
	p := upgradeResult(t, f.call(f.body(), f.admin, nil))
	if p.Improvement || p.Comparable || !strings.Contains(p.Reason, "candidate_quality_rejected") {
		t.Fatal(p)
	}
	if before != f.state(t) {
		t.Fatal("attribute rejection wrote state")
	}
}
