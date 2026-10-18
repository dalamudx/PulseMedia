package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func sendProjectionWanted(t *testing.T, f *upgradeWorkerFixture) (uuid.UUID, []byte) {
	t.Helper()
	var id uuid.UUID
	var raw []byte
	if err := f.p.QueryRow(context.Background(), `SELECT w.id,to_jsonb(w) FROM wanted_item w WHERE subscription_id=$1`, f.sub).Scan(&id, &raw); err != nil {
		t.Fatal(err)
	}
	return id, raw
}
func projectSendWanted(t *testing.T, f *upgradeWorkerFixture, id uuid.UUID) error {
	t.Helper()
	ctx := context.Background()
	sub, err := postgres.Queries(f.p).GetSubscriptionByID(ctx, f.sub)
	if err != nil {
		t.Fatal(err)
	}
	_, err = postgres.NewWantedProjectionStore(f.p).ProjectWanted(ctx, id, sub.QualityPolicyID, sub.QualityPolicyRevision)
	return err
}
func TestMovieUpgradeSendProjectionDefersAndResumesPG(t *testing.T) {
	f := upgradeWorkerNative(t, "Ok.")
	ctx := context.Background()
	wanted, before := sendProjectionWanted(t, f)
	if err := projectSendWanted(t, f, wanted); !errors.Is(err, ports.ErrWantedProjectionChanged) {
		t.Fatalf("live command must defer projection: %v", err)
	}
	_, after := sendProjectionWanted(t, f)
	if !bytes.Equal(before, after) {
		t.Fatal("deferred projection changed Wanted")
	}
	// Deferred items must not consume the bounded candidate batch.
	media, unit, sub, other := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO media(id,media_type,title) VALUES($1,'movie','Other film')`, media)
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')`, unit, media)
	f.exec(t, `INSERT INTO subscription(id,user_id,media_id,root_id,quality_policy_id,quality_policy_revision,indexer_ids,downloader_id,state,intent_version,scope_type) SELECT $1,user_id,$2,root_id,quality_policy_id,quality_policy_revision,indexer_ids,downloader_id,state,intent_version,scope_type FROM subscription WHERE id=$3`, sub, media, f.sub)
	f.exec(t, `INSERT INTO wanted_item(id,subscription_id,unit_id,intent_version,is_skipped,is_satisfied,next_evaluate_at) VALUES($1,$2,$3,1,false,false,clock_timestamp()-interval '1 hour')`, other, sub, unit)
	rows, err := postgres.Queries(f.p).ListPendingEvaluationWantedItems(ctx, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != other {
		t.Fatalf("deferred Wanted starved unrelated batch: count=%d err=%v", len(rows), err)
	}
	lease := claimSendJob(t, f.nativeUpgradeFixture, f.cmd)
	q := jobs.NewQueue(f.p, idgen.New(), nativeUpgradeClock{})
	if err = q.Fail(ctx, lease.Lease.ID, lease.Lease.Owner, lease.Lease.Token, "controlled test terminal", time.Second); err != nil {
		t.Fatal(err)
	}
	if err = projectSendWanted(t, f, wanted); err != nil {
		t.Fatalf("terminal job blocked projection: %v", err)
	}
	_, after = sendProjectionWanted(t, f)
	if bytes.Equal(before, after) {
		t.Fatal("terminal job left projection unchanged")
	}
	f.assertNoDispatch(t)
}
func TestMovieUpgradeSendProjectionExpiredClaimResumesPG(t *testing.T) {
	f := upgradeWorkerNative(t, "Ok.")
	ctx := context.Background()
	wanted, _ := sendProjectionWanted(t, f)
	q := jobs.NewQueue(f.p, idgen.New(), nativeUpgradeClock{})
	job, err := q.Claim(ctx, []string{ports.MovieUpgradeSendJobKind}, "projection-expiry", 150*time.Millisecond)
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err = f.p.Exec(ctx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM lease_until-clock_timestamp()))+0.02) FROM job WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = projectSendWanted(t, f, wanted); err != nil {
		t.Fatalf("expired claim blocked projection: %v", err)
	}
	var state string
	if err = f.p.QueryRow(ctx, `SELECT state FROM job WHERE id=$1`, job.ID).Scan(&state); err != nil || state != "running" {
		t.Fatalf("projection adopted expired job: %s %v", state, err)
	}
	f.assertNoDispatch(t)
}
func TestMovieUpgradeSendProjectionExplicitMutationPG(t *testing.T) {
	f := upgradeWorkerNative(t, "Ok.")
	wanted, _ := sendProjectionWanted(t, f)
	f.exec(t, `UPDATE wanted_item SET is_skipped=true WHERE id=$1`, wanted)
	deferred, err := postgres.Queries(f.p).IsMovieUpgradeSendProjectionDeferred(context.Background(), wanted)
	if err != nil || deferred {
		t.Fatalf("explicit mutation still deferred: %v %v", deferred, err)
	}
	lease := claimSendJob(t, f.nativeUpgradeFixture, f.cmd)
	req := lease
	if err = f.executor(t, torznab.CompleteTVFetcher{}).ExecuteMovieUpgradeSendJob(context.Background(), req, uuid.MustParse(f.actor.ID)); err == nil {
		t.Fatal("manual Wanted change did not fence send")
	}
	if f.fetches.Load() != 0 {
		t.Fatal("manual change reached source fetch")
	}
	f.assertNoDispatch(t)
}
