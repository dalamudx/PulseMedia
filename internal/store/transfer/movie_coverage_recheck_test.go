package transfer

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"testing"
)

func TestMovieOrganizationCannotResignObsoleteCoverage(t *testing.T) {
	unit := uuid.New()
	for _, matched := range []bool{true, false} {
		row := db.GetDownloadOrganizationInputsRow{UnitID: unit, FrozenCoverage: true, CandidateHash: "hash", LinkEncrypted: []byte("opaque"), QualityProfile: []byte(`{"resolution":"1080p"}`)}
		if matched {
			row.MatchedUnitID = pgtype.UUID{Bytes: unit, Valid: true}
		}
		for _, raw := range []string{
			"Fixture.1999.02.14.1080p.WEB-DL.x264.{tmdb-550}",
			"Fixture.19990214.1080p.WEB-DL.x264.{tmdb-550}",
			"Fixture.1999.S01E01.1080p.WEB-DL.x264",
			"[Team] Fixture - 25 [1080p]",
			"Fixture.1999.1080p.WEB-DL.sample.x264",
		} {
			row.CandidateTitle = raw
			row.AcquisitionSourceStamp = quality.AcquisitionSourceStamp(raw, row.CandidateHash, row.LinkEncrypted, row.QualityProfile)
			if trustedMovieCoverage(row) {
				t.Errorf("old matched/frozen coverage re-signed at plan creation: matched=%v raw=%s", matched, raw)
			}
		}
		row.CandidateTitle = "Fixture.1999.1080p.WEB-DL.x264"
		row.AcquisitionSourceStamp = quality.AcquisitionSourceStamp(row.CandidateTitle, row.CandidateHash, row.LinkEncrypted, row.QualityProfile)
		if !trustedMovieCoverage(row) {
			t.Error("genuine current movie lost original frozen authority")
		}
	}
}
