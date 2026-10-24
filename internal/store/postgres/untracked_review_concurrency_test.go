package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestUntrackedReviewConcurrentDifferentBodiesSameKeyPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	type outcome struct {
		receipt *ports.UntrackedReviewRequestReceipt
		err     error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, id := range []int64{0, 42} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			in := f.input
			in.TMDBID = id
			r, e := f.review.RequestUntrackedReview(ctx, f.a, f.scope, f.file.ID, in, "raced-body")
			results <- outcome{r, e}
		}(id)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		if result.err == nil && result.receipt != nil {
			successes++
		} else if errors.Is(result.err, ports.ErrEndpointVersionConflict) {
			conflicts++
		} else {
			t.Fatal(result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	f.count(t, `SELECT count(*) FROM untracked_review_request`, 1)
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_review'`, 1)
}

func TestUntrackedReviewLeaseDeadlineCannotBeRenewedByReloadingMaterialPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	f.requestReview(t, "deadline")
	j, err := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.UntrackedReviewJobKind}, Column2: "deadline-worker", Column3: 120000})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '2 seconds' WHERE id=$1`, j.ID)
	c, err := f.review.LoadUntrackedReview(t.Context(), ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '120 seconds' WHERE id=$1`, j.ID)
	f.exec(t, `CREATE FUNCTION review_fixed_deadline_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(2.05); RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER review_fixed_deadline_tail AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.action='untracked.review.completed') EXECUTE FUNCTION review_fixed_deadline_tail()`)
	checkinErr(t, f.review.CompleteUntrackedReview(t.Context(), c, reviewMovieEvidence()), ports.ErrUntrackedReviewStale)
	f.count(t, `SELECT count(*) FROM untracked_review_result`, 0)
	f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='running'`, 1, j.ID)
}

func TestUntrackedReviewRecoveryDeadAndOtherKindsUntouchedPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	f.requestReview(t, "exhausted")
	c := f.claimReview(t)
	id := c.UntrackedReviewSnapshot().Lease.ID
	f.exec(t, `UPDATE job SET attempts=3,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, id)
	foreign := uuid.New()
	f.exec(t, `INSERT INTO job(id,kind,state,attempts,max_attempts,lease_owner,lease_until) VALUES($1,'movie_recognition','running',1,3,'other',clock_timestamp()-interval '1 second')`, foreign)
	n, err := f.review.RecoverUntrackedReviews(t.Context(), 64)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='dead' AND lease_owner IS NULL AND lease_until IS NULL`, 1, id)
	f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='running' AND lease_owner='other'`, 1, foreign)
}

func TestUntrackedReviewMissingConfirmationReviewFailsClosedPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	v := f.completeReview(t, "review")
	in := reviewConfirmation(v)
	c := f.prepareConfirmation(t, in, "confirm")
	if _, err := f.review.CommitUntrackedConfirmation(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `ALTER TABLE untracked_review_request_receipt DISABLE TRIGGER untracked_review_receipt_immutable`)
	f.exec(t, `DELETE FROM untracked_review_request_receipt WHERE request_id=$1`, v.Request.ID)
	_, err := f.review.ReadUntrackedConfirmationReceipt(t.Context(), f.a, f.scope, f.file.ID, "confirm")
	checkinErr(t, err, ports.ErrUntrackedReviewStale)
	_, err = f.review.PrepareUntrackedConfirmation(t.Context(), f.a, f.scope, f.file.ID, in, "confirm")
	checkinErr(t, err, ports.ErrUntrackedReviewStale)
	f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 1)
}

func TestUntrackedReviewConfirmationReadActorAndNoSQLLeakPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	v := f.completeReview(t, "review")
	in := reviewConfirmation(v)
	c := f.prepareConfirmation(t, in, "confirm")
	if _, err := f.review.CommitUntrackedConfirmation(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	other := f.user(t)
	token := *f.a
	token.Scopes = []string{}
	member := *f.a
	member.Role = "member"
	for _, a := range []*ports.Actor{other, &token, &member, nil} {
		if _, err := f.review.ReadUntrackedConfirmationReceipt(t.Context(), a, f.scope, f.file.ID, "confirm"); err == nil {
			t.Fatal("receipt visible to unauthorized actor")
		}
		if _, err := f.review.PrepareUntrackedConfirmation(t.Context(), a, f.scope, f.file.ID, in, "confirm"); err == nil {
			t.Fatal("confirmation replay elevated actor")
		}
	}
	f.exec(t, `ALTER TABLE untracked_review_result RENAME TO review_result_fixture_hidden`)
	_, err := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, v.Request.ID)
	checkinErr(t, err, ports.ErrUnavailable)
	if err.Error() != ports.ErrUnavailable.Error() {
		t.Fatal("database details escaped", err)
	}
}
