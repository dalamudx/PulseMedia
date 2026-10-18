package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestCompleteSeasonMetadataRequiresExactCurrentSet(t *testing.T) {
	mediaID, first, third := uuid.New(), uuid.New(), uuid.New()
	frozen := []ports.SeasonMetadataUnit{{UnitID: first, UnitVersion: 4, TMDBEpisodeID: 1001, EpisodeNumber: 1}, {UnitID: third, UnitVersion: 8, TMDBEpisodeID: 1003, EpisodeNumber: 3}}
	raw, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	base := db.TvSeasonMetadataSnapshot{MediaID: mediaID, MediaVersion: 9, SeasonNumber: 1, SourceTmdbID: 100, Units: raw, ContentHash: hex.EncodeToString(digest[:])}
	source := ports.EpisodeSourceContext{MediaID: mediaID, MediaVersion: 9, TMDBID: 100, Units: []ports.EpisodeSourceUnit{
		{ID: first, Version: 4, EpisodeID: 1001, Season: 1, Episode: 1, Key: "S01E01"},
		{ID: third, Version: 8, EpisodeID: 1003, Season: 1, Episode: 3, Key: "S01E03"},
	}}
	if proof := currentCompleteSeasonMetadata(base, &source); proof == nil || len(proof.Units) != 2 || proof.Units[1].EpisodeNumber != 3 {
		t.Fatal("explicit noncontiguous set was lost or expanded", proof)
	}
	for _, name := range []string{"new-media-version", "wrong-source", "wrong-media", "special-season", "bad-hash", "invalid-json", "no-units", "missing-active-unit", "extra-middle-unit", "retired-unit", "new-unit-version", "new-provider-identity", "unbound-extra-canonical-key", "mismatched-extra-season", "changed-coordinate", "changed-key", "duplicate-active-identity", "resigned-incomplete-set", "resigned-duplicate-coverage"} {
		t.Run(name, func(t *testing.T) {
			row, current := base, source
			current.Units = append([]ports.EpisodeSourceUnit(nil), source.Units...)
			switch name {
			case "new-media-version":
				current.MediaVersion++
			case "wrong-source":
				current.TMDBID++
			case "wrong-media":
				current.MediaID = uuid.New()
			case "special-season":
				row.SeasonNumber = 0
			case "bad-hash":
				row.ContentHash = "wrong"
			case "invalid-json":
				row.Units = []byte(`{`)
			case "no-units":
				row.Units = []byte(`[]`)
			case "missing-active-unit":
				current.Units = current.Units[:1]
			case "extra-middle-unit":
				current.Units = append(current.Units, ports.EpisodeSourceUnit{ID: uuid.New(), Version: 1, Season: 1, Episode: 2, Key: "S01E02"})
			case "retired-unit":
				current.Units[0].Retired = true
			case "new-provider-identity":
				current.Units[0].EpisodeID++
			case "unbound-extra-canonical-key":
				current.Units = append(current.Units, ports.EpisodeSourceUnit{ID: uuid.New(), Version: 1, Season: -1, Episode: -1, Key: "S01E02"})
			case "mismatched-extra-season":
				current.Units = append(current.Units, ports.EpisodeSourceUnit{ID: uuid.New(), Version: 1, Season: 2, Episode: 2, Key: "S01E02"})
			case "new-unit-version":
				current.Units[0].Version++
			case "changed-coordinate":
				current.Units[0].Episode = 2
			case "changed-key":
				current.Units[0].Key = "S01E02"
			case "duplicate-active-identity":
				current.Units[1].ID = first
			case "resigned-incomplete-set", "resigned-duplicate-coverage":
				values := append([]ports.SeasonMetadataUnit(nil), frozen...)
				if name == "resigned-incomplete-set" {
					values = values[:1]
				} else {
					values[1] = values[0]
				}
				row.Units, err = json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(row.Units)
				row.ContentHash = hex.EncodeToString(sum[:])
			}
			if currentCompleteSeasonMetadata(row, &current) != nil {
				t.Fatal("stale/incomplete evidence became complete", name)
			}
		})
	}
	var normalized []map[string]any
	if json.Unmarshal(raw, &normalized) != nil {
		t.Fatal("fixture JSON")
	}
	base.Units, err = json.MarshalIndent(normalized, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if currentCompleteSeasonMetadata(base, &source) == nil {
		t.Fatal("JSONB representation invalidated identical typed evidence")
	}
}
