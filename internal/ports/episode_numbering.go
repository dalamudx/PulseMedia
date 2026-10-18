package ports

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

// ValidNumberingShape checks the frozen DTO shape only; it is never execution authorization.
func (s EpisodeExecutionSnapshot) ValidNumberingShape() bool {
	if s.ParserVersion != media.EpisodeParserVersion {
		return false
	}
	if s.Kind == "reviewed_standard_episode" {
		return s.MappingMode == "standard" && s.SourceNumber == 0 && len(s.MappingRows) == 0
	}
	if s.Kind != "reviewed_mapped_episode" || (s.MappingMode != "absolute" && s.MappingMode != "manual") || s.MappingRevision < 1 || s.SourceNumber < 1 || s.SourceNumber > 9999 || len(s.MappingRows) == 0 || len(s.MappingRows) > 5000 || !numberingDigest(s.MappingSourceHash) || !numberingDigest(s.MappingInputStamp) {
		return false
	}
	if s.MappingMode == "absolute" && s.MappingValidUntil == nil || s.MappingMode == "manual" && s.MappingValidUntil != nil {
		return false
	}
	return true
}
func numberingDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func (s EpisodeExecutionSnapshot) ResolveNumbering(parsed media.EpisodeRecognition, canonical []media.NumberingUnit) ([]media.NumberingRow, string) {
	if !s.ValidNumberingShape() {
		return nil, "invalid_numbering_evidence"
	}
	if s.MappingMode == "standard" {
		return media.ResolveStandardSingleEpisode(parsed, canonical)
	}
	if parsed.AbsoluteEpisode != s.SourceNumber {
		return nil, "source_number_changed"
	}
	rows := []media.NumberingRow{}
	for _, r := range s.MappingRows {
		rows = append(rows, media.NumberingRow{SourceNumber: r.SourceNumber, EpisodeID: r.EpisodeID, UnitID: r.UnitID, UnitKey: r.UnitKey, UnitVersion: r.UnitVersion})
	}
	return media.ResolveApprovedAbsoluteSingle(parsed, s.MappingMode, rows, canonical)
}
