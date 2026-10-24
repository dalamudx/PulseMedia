package postgres_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMovieUpgradeWatchOriginalSQLGatePG(t *testing.T) {
	f := newMovieUpgradeWatchFixture(t)
	before := f.snapshot(t)
	r := f.capture.MovieUpgradeTrackingHintRequest()
	tx, e := f.p.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	id := uuid.New()
	var queued json.RawMessage
	e = tx.QueryRow(t.Context(), `WITH moment AS (SELECT clock_timestamp() AS now) INSERT INTO job(id,kind,dedupe_key,priority,state,payload,attempts,max_attempts,lease_token,not_before,created_at,updated_at) SELECT $1,'movie_upgrade_tracking',$2,0,'queued',jsonb_build_object('tracking_job_id',$1::uuid),0,1,0,now,now,now FROM moment RETURNING to_jsonb(job)`, id, "movie-upgrade-tracking:"+r.OperationID.String()+":"+r.PriorObservationID.String()).Scan(&queued)
	if e != nil {
		t.Fatal("construct original-format job", e)
	}
	_, e = tx.Exec(t.Context(), `INSERT INTO movie_upgrade_tracking_job(job_id,operation_id,task_id,actor_id,prior_observation_id,queued_job) VALUES($1,$2,$3,$4,$5,$6)`, id, r.OperationID, r.TaskID, uuid.MustParse(f.actor.ID), r.PriorObservationID, queued)
	if e == nil || !strings.Contains(e.Error(), "tracking predecessor not ready") {
		t.Fatal("original 30s trigger not enforced", e)
	}
	if e = tx.Rollback(t.Context()); e != nil {
		t.Fatal(e)
	}
	if f.snapshot(t) != before {
		t.Fatal("failed early binding leaked job")
	}
}
