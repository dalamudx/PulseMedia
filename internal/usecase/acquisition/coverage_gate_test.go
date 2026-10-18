package acquisition_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"testing"
)

func TestMovieUoWRefusesTVPackAbsoluteAndUnprovenCoverageBeforeOccupancy(t *testing.T) {
	svc, q, root, unit, candidate := setupAcquisitionTest(t)
	ctx := context.Background()
	original, e := q.GetCandidateByID(ctx, candidate)
	if e != nil {
		t.Fatal(e)
	}
	actor := pgtest.CreateUserActor(t, q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	for i, title := range []string{"Fight.Club.S01E01.1999.1080p.mkv", "Fight.Club.S01E01E03.1999.1080p.mkv", "[Subs] Fight Club - 25 [1080p].mkv", "Fight.Club.1080p.mkv", "https://private.test/passkey=secret"} {
		id := uuid.New()
		if _, e = q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: id, IndexerID: original.IndexerID, Title: title, Guid: uuid.NewString(), SourceConfigVersion: original.SourceConfigVersion, LinkEncrypted: []byte("fixture-cipher"), QualityProfile: []byte(`{}`), SizeBytes: 100}); e != nil {
			t.Fatal(e)
		}
		if _, e = svc.AcquireMovie(ctx, actor, acquisition.AcquireMovieCommand{RootID: root, UnitIDs: []uuid.UUID{unit}, CandidateID: id}); !errors.Is(e, acquisition.ErrUnsupportedCoverage) {
			t.Fatal("source coverage bypassed", i, e)
		}
		if _, e = q.GetActiveReservation(ctx, db.GetActiveReservationParams{UnitID: unit, RootID: root, Purpose: "manual_acquisition"}); e == nil {
			t.Fatal("rejected source created occupancy", i)
		}
	}
}
func TestMovieUoWRefusesSingleTVUnitDespiteOtherwiseValidMovieSource(t *testing.T) {
	svc, q, root, _, candidate := setupAcquisitionTest(t)
	ctx := context.Background()
	m, u := uuid.New(), uuid.New()
	if _, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: m, Title: "TV", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: u, MediaID: m, UnitKey: "S01E01"}); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.AcquireMovie(ctx, pgtest.CreateUserActor(t, q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}), acquisition.AcquireMovieCommand{RootID: root, UnitIDs: []uuid.UUID{u}, CandidateID: candidate}); !errors.Is(e, acquisition.ErrUnsupportedCoverage) {
		t.Fatal("TV unit bypassed root", e)
	}
	if _, e := q.GetActiveReservation(ctx, db.GetActiveReservationParams{UnitID: u, RootID: root, Purpose: "manual_acquisition"}); e == nil {
		t.Fatal("unsupported TV got physical protection")
	}
}
