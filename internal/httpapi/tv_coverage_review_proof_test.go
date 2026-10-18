package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
)

func TestTVCoverageReviewNativePGIncompleteAndChangedProof(t *testing.T) {
	for _, name := range []string{"changed-proof", "resigned-incomplete-proof"} {
		t.Run(name, func(t *testing.T) {
			f := newCoverageFixture(t)
			c := f.command(f.preview(t))
			// Independent malformed proof fixture: the complete provider response has TWO rows,
			// but this deliberately stores only its first row with a correct content hash.
			if name == "resigned-incomplete-proof" {
				raw, e := json.Marshal([]ports.SeasonMetadataUnit{{UnitID: f.f.units[0], UnitVersion: 2, TMDBEpisodeID: 1001, EpisodeNumber: 1}})
				if e != nil {
					t.Fatal(e)
				}
				sum := sha256.Sum256(raw)
				f.exec(t, "UPDATE tv_season_metadata_snapshot SET units=$2,content_hash=$3 WHERE media_id=(SELECT media_id FROM subscription WHERE id=$1)", f.f.sub.Id, raw, hex.EncodeToString(sum[:]))
			} else {
				f.exec(t, "UPDATE tv_season_metadata_snapshot SET content_hash=repeat('0',64) WHERE media_id=(SELECT media_id FROM subscription WHERE id=$1)", f.f.sub.Id)
			}
			if f.preview(t).CanReview {
				t.Fatal("incomplete or changed proof became current")
			}
			if _, e := f.svc.Review(context.Background(), f.actor, c, "bad-proof"); !errors.Is(e, ports.ErrTVCoverageReviewConflict) {
				t.Fatal("proof change accepted", e)
			}
		})
	}
}
