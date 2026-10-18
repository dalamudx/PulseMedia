package ports

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestFrozenMappedNumberingRejectsSourceSwitchAndMissingSeal(t *testing.T) {
	id := uuid.New()
	until := time.Now().Add(time.Hour)
	base := EpisodeExecutionSnapshot{Kind: "reviewed_mapped_episode", MappingMode: "absolute", MappingRevision: 1, MappingSourceHash: strings.Repeat("a", 64), MappingInputStamp: strings.Repeat("b", 64), MappingValidUntil: &until, SourceNumber: 25, ParserVersion: media.EpisodeParserVersion, MappingRows: []EpisodeGroupMappingRow{{SourceNumber: 25, EpisodeID: 1001, UnitID: id, UnitKey: "S02E01", UnitVersion: 2}}}
	units := []media.NumberingUnit{{ID: id, EpisodeID: 1001, Key: "S02E01", Version: 2, Season: 2, Episode: 1}}
	if rows, _ := base.ResolveNumbering(media.RecognizeRelease("[Team] Fixture - 25 [1080p]"), units); len(rows) != 1 || rows[0].UnitID != id {
		t.Fatal(rows)
	}
	if rows, _ := base.ResolveNumbering(media.RecognizeRelease("[Team] Fixture - 27 [1080p]"), units); len(rows) != 0 {
		t.Fatal("original source number silently changed", rows)
	}
	for _, mutation := range []string{"missing-hash", "missing-input", "missing-expiry", "kind-standard", "missing-revision", "missing-rows"} {
		t.Run(mutation, func(t *testing.T) {
			s := base
			switch mutation {
			case "missing-hash":
				s.MappingSourceHash = ""
			case "missing-input":
				s.MappingInputStamp = ""
			case "missing-expiry":
				s.MappingValidUntil = nil
			case "kind-standard":
				s.Kind = "reviewed_standard_episode"
			case "missing-revision":
				s.MappingRevision = 0
			case "missing-rows":
				s.MappingRows = nil
			}
			if s.ValidNumberingShape() {
				t.Fatal("unsealed mapped evidence accepted")
			}
		})
	}
}
