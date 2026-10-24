package library

import "testing"

func TestUntrackedReviewTVPreservesUnknownAirDate(t *testing.T) {
	f := untrackedReviewFixture()
	f.tv()
	f.path("Example.2020.S01E01E03.1080p.mkv")
	unknownID := f.metadata.season.Episodes[0].TMDBEpisodeID
	f.metadata.season.Episodes[0].AirDate = nil
	if err := f.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	evidence := f.execution.evidence
	if !evidence.Complete || evidence.Reason != "ready" || evidence.Candidate == nil || len(evidence.Candidate.Units) != 2 || !untrackedReviewCandidateValid(evidence.Candidate, "tv") {
		t.Fatal("unknown date rejected exact sparse identity")
	}
	unknown, known := 0, 0
	for _, u := range evidence.Candidate.Units {
		if u.TMDBEpisodeID == unknownID {
			unknown++
			if u.AirDate != nil {
				t.Fatal("invented air date")
			}
		} else {
			known++
			if u.AirDate == nil {
				t.Fatal("lost known date")
			}
			for _, original := range f.metadata.season.Episodes {
				if original.TMDBEpisodeID == u.TMDBEpisodeID && (original.AirDate == u.AirDate || !original.AirDate.Equal(*u.AirDate)) {
					t.Fatal("known date aliases source or changed value")
				}
			}
		}
	}
	if unknown != 1 || known != 1 {
		t.Fatal("lost sparse episode identity")
	}
	untrackedReviewAssertClosed(t, f.files)
}
