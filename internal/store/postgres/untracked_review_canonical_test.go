package postgres_test

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
)

func canonicalReviewFixture(t *testing.T) (*untrackedReviewFixture, uuid.UUID) {
	t.Helper()
	f := newUntrackedReviewFixture(t, "tv")
	mid, unit := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO media(id,title,original_title,year,media_type) VALUES($1,'Series','Series',2020,'tv')`, mid)
	f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.tv','123')`, uuid.New(), mid)
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) VALUES($1,$2,'S01E07',1,7)`, unit, mid)
	f.exec(t, `INSERT INTO media_unit_identity(unit_id,namespace,external_id,tmdb_series_id) VALUES($1,'tmdb.episode',1007,123)`, unit)
	options, err := f.review.ReadUntrackedReviewEpisodeOptions(t.Context(), f.a, f.scope, f.file.ID, 123, 1)
	if err != nil || len(options) != 1 {
		t.Fatal(options, err)
	}
	o := options[0]
	f.input.TMDBID = 123
	f.input.CanonicalUnitID = o.CanonicalUnitID
	f.input.MediaMetadataVersion = o.MediaMetadataVersion
	f.input.UnitMetadataVersion = o.UnitMetadataVersion
	f.input.Season = o.Season
	f.input.Episode = o.Episode
	f.input.TMDBEpisodeID = o.TMDBEpisodeID
	return f, mid
}

func canonicalReviewEvidence() ports.UntrackedReviewEvidence {
	return ports.UntrackedReviewEvidence{ParserVersion: "fixture-v1", Selection: "explicit", Complete: true, Reason: "ready", Candidate: &ports.UntrackedReviewCandidate{Key: "tmdb.tv:123", Namespace: "tmdb.tv", TMDBID: 123, Title: "Series", OriginalTitle: "Series", Year: 2020, Units: []ports.UntrackedReviewUnit{{UnitKey: "S01E07", Season: 1, Episode: 7, TMDBEpisodeID: 1007}}}}
}

func TestUntrackedReviewCanonicalProofAndDuplicateKeyPG(t *testing.T) {
	f, _ := canonicalReviewFixture(t)
	original := f.requestReview(t, "canonical")
	duplicate := f.requestReview(t, "canonical")
	if !sameHistoricalJSON(original, duplicate) {
		t.Fatal("duplicate key changed receipt")
	}
	changed := f.input
	changed.UnitMetadataVersion++
	_, err := f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, changed, "canonical")
	checkinErr(t, err, ports.ErrEndpointVersionConflict)
	capture := f.claimReview(t)
	if err = f.review.CompleteUntrackedReview(t.Context(), capture, canonicalReviewEvidence()); err != nil {
		t.Fatal(err)
	}
	v, err := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, original.Request.ID)
	if err != nil || v.Stale || v.State != "ready" {
		t.Fatal(v, err)
	}
	p := f.prepareConfirmation(t, reviewConfirmation(v), "confirm")
	if _, err = f.review.CommitUntrackedConfirmation(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	f.count(t, `SELECT count(*) FROM untracked_review_request`, 1)
	f.count(t, `SELECT count(*) FROM media_unit`, 1)
	f.count(t, `SELECT count(*) FROM unit_fact`, 0)
}

func TestUntrackedReviewCanonicalWrongProofPG(t *testing.T) {
	f, _ := canonicalReviewFixture(t)
	for _, which := range []string{"work", "season", "episode", "identity", "unit", "media_version", "unit_version"} {
		t.Run(which, func(t *testing.T) {
			in := f.input
			switch which {
			case "work":
				in.TMDBID = 124
			case "season":
				in.Season = 2
			case "episode":
				in.Episode = 8
			case "identity":
				in.TMDBEpisodeID = 1008
			case "unit":
				in.CanonicalUnitID = uuid.NewString()
			case "media_version":
				in.MediaMetadataVersion++
			case "unit_version":
				in.UnitMetadataVersion++
			}
			_, err := f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, in, "wrong-"+which)
			checkinErr(t, err, ports.ErrUntrackedReviewStale)
		})
	}
	f.count(t, `SELECT count(*) FROM untracked_review_request`, 0)
}

func TestUntrackedReviewCanonicalMetadataInvalidatesPreviewPG(t *testing.T) {
	for _, which := range []string{"media", "unit", "retired", "file", "revoked"} {
		t.Run(which, func(t *testing.T) {
			f, mid := canonicalReviewFixture(t)
			req := f.requestReview(t, "ready")
			capture := f.claimReview(t)
			if err := f.review.CompleteUntrackedReview(t.Context(), capture, canonicalReviewEvidence()); err != nil {
				t.Fatal(err)
			}
			v, err := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, req.Request.ID)
			if err != nil {
				t.Fatal(err)
			}
			confirmation := f.prepareConfirmation(t, reviewConfirmation(v), "stale-confirm")
			switch which {
			case "media":
				f.exec(t, `UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1`, mid)
			case "unit":
				f.exec(t, `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1`, uuid.MustParse(f.input.CanonicalUnitID))
			case "retired":
				f.exec(t, `UPDATE media_unit SET retired=true WHERE id=$1`, uuid.MustParse(f.input.CanonicalUnitID))
			case "file":
				f.exec(t, `UPDATE untracked_discovered_file SET revision=revision+1 WHERE id=$1`, f.file.ID)
			case "revoked":
				f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID))
			}
			_, err = f.review.CommitUntrackedConfirmation(t.Context(), confirmation)
			if err == nil {
				t.Fatal("stale confirmation committed")
			}
			f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 0)
			if which != "revoked" {
				stale, e := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, req.Request.ID)
				if e != nil || !stale.Stale {
					t.Fatal(stale, e)
				}
				receipt, e := f.review.ReadUntrackedReviewRequestReceipt(t.Context(), f.a, f.scope, f.file.ID, "ready")
				if e != nil || !sameHistoricalJSON(receipt, req) {
					t.Fatal("historical proof changed", e)
				}
			}
		})
	}
}
