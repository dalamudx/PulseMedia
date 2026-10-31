package ports

import (
	"encoding/json"
	"github.com/google/uuid"
	"testing"
)

func TestUntrackedReviewCanonicalLegacyJSON(t *testing.T) {
	in := UntrackedReviewInput{ScopeVersion: 1, FileRevision: 2, MediaType: "tv", TMDBID: 123}
	raw, err := json.Marshal(in)
	if err != nil || string(raw) != `{"scope_version":1,"file_revision":2,"media_type":"tv","tmdb_id":123}` {
		t.Fatal("legacy input bytes changed", string(raw), err)
	}
	if !in.CanonicalSelectionValid() {
		t.Fatal("legacy selection rejected")
	}
}

func TestUntrackedReviewCanonicalProofContract(t *testing.T) {
	in := UntrackedReviewInput{ScopeVersion: 1, FileRevision: 2, MediaType: "tv", TMDBID: 123, CanonicalUnitID: uuid.NewString(), MediaMetadataVersion: 7, UnitMetadataVersion: 9, Season: 1, Episode: 7, TMDBEpisodeID: 1007}
	if !in.CanonicalSelectionValid() {
		t.Fatal("valid proof rejected")
	}
	for _, field := range []string{"id", "media_version", "unit_version", "season", "episode", "episode_id", "movie", "automatic", "zero_uuid"} {
		t.Run(field, func(t *testing.T) {
			bad := in
			switch field {
			case "id":
				bad.CanonicalUnitID = ""
			case "media_version":
				bad.MediaMetadataVersion = 0
			case "unit_version":
				bad.UnitMetadataVersion = 0
			case "season":
				bad.Season = 0
			case "episode":
				bad.Episode = 0
			case "episode_id":
				bad.TMDBEpisodeID = 0
			case "movie":
				bad.MediaType = "movie"
			case "automatic":
				bad.TMDBID = 0
			case "zero_uuid":
				bad.CanonicalUnitID = uuid.Nil.String()
			}
			if bad.CanonicalSelectionValid() {
				t.Fatal("partial or invalid proof accepted")
			}
		})
	}
	candidate := &UntrackedReviewCandidate{Namespace: "tmdb.tv", TMDBID: 123, Units: []UntrackedReviewUnit{{Season: 1, Episode: 7, TMDBEpisodeID: 1007}}}
	if !in.CanonicalCandidateMatches(candidate) {
		t.Fatal("exact candidate rejected")
	}
	candidate.Units = append(candidate.Units, UntrackedReviewUnit{Season: 1, Episode: 8, TMDBEpisodeID: 1008})
	if in.CanonicalCandidateMatches(candidate) {
		t.Fatal("extra episode accepted")
	}
}
