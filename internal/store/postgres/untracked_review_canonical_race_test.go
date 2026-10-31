package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestUntrackedReviewCanonicalCurrentVersionRacePG(t *testing.T) {
	f, mid := canonicalReviewFixture(t)
	var pgVersion int
	if err := f.p.QueryRow(t.Context(), `SELECT current_setting('server_version_num')::integer`).Scan(&pgVersion); err != nil || pgVersion != 180006 {
		t.Fatalf("PG=%d err=%v, want 18.6", pgVersion, err)
	}
	req := f.requestReview(t, "race")
	capture := f.claimReview(t)
	if err := f.review.CompleteUntrackedReview(t.Context(), capture, canonicalReviewEvidence()); err != nil {
		t.Fatal(err)
	}
	v, err := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, req.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	confirmation := f.prepareConfirmation(t, reviewConfirmation(v), "race-confirm")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var confirmErr, updateErr error
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, confirmErr = f.review.CommitUntrackedConfirmation(ctx, confirmation)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, updateErr = f.p.Exec(ctx, `UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1`, mid)
	}()
	close(start)
	wg.Wait()
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	if confirmErr != nil {
		checkinErr(t, confirmErr, ports.ErrUntrackedReviewStale)
		f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 0)
	} else {
		f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 1)
	}
	after, err := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, req.Request.ID)
	if err != nil || !after.Stale {
		t.Fatal("race left current preview", after, err)
	}
	f.count(t, `SELECT count(*) FROM unit_fact`, 0)
}

func TestUntrackedReviewCanonicalWorkerLateMetadataPG(t *testing.T) {
	f, _ := canonicalReviewFixture(t)
	f.requestReview(t, "late")
	capture := f.claimReview(t)
	f.exec(t, `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1`, uuid.MustParse(f.input.CanonicalUnitID))
	err := f.review.CompleteUntrackedReview(t.Context(), capture, canonicalReviewEvidence())
	checkinErr(t, err, ports.ErrUntrackedReviewStale)
	f.count(t, `SELECT count(*) FROM untracked_review_result`, 0)
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_review' AND state='succeeded'`, 0)
}

func TestUntrackedReviewCanonicalOptionAuthorizationPG(t *testing.T) {
	f, _ := canonicalReviewFixture(t)
	other := f.user(t)
	_, err := f.review.ReadUntrackedReviewEpisodeOptions(t.Context(), other, f.scope, f.file.ID, 123, 1)
	checkinErr(t, err, ports.ErrForbidden)
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID))
	_, err = f.review.ReadUntrackedReviewEpisodeOptions(t.Context(), f.a, f.scope, f.file.ID, 123, 1)
	checkinErr(t, err, ports.ErrForbidden)
}
