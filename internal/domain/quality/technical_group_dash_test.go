package quality

import "testing"

func TestTechnicalSourceDashIsNotRawRejectedGroup(t *testing.T) {
	for _, tc := range []struct{ raw, group string }{
		{"Fixture.2020.1080p.WEB-DL.mkv", "DL"},
		{"Fixture.2020.1080p.WEB-DL[ettv].mkv", "DL"},
		{"Fixture.2020.1080p.Blu-Ray.mkv", "Ray"},
		{"Fixture.2020.1080p.DTS-HD.mkv", "HD"},
	} {
		if rejectedRawReleaseGroup(tc.raw, tc.group) {
			t.Fatal("technical marker acquired negative group identity", tc)
		}
	}
	for _, raw := range []string{"Fixture.2020.1080p.WEB-DL-DL.mkv", "Fixture.2020.1080p.x264-DL.mkv"} {
		if !rejectedRawReleaseGroup(raw, "DL") {
			t.Fatal("actual denied group became eligible", raw)
		}
	}
	if ReleaseGroupDelimiter("Fixture.2020.1080p.NOTWEB-DL") < 0 {
		t.Fatal("technical prefix inferred from word substring")
	}
	if !RejectedReleaseGroup("DL", "DL") {
		t.Fatal("historical group evidence re-signed")
	}
}
