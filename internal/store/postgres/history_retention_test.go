package postgres_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

// Seed valid terminal history at its original time, including the original gate
// and deferred commit graph. No production evidence or trigger is weakened.
func seedRetentionObservation(t *testing.T, f *globalRateFixture, state string, days int) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	id := uuid.New()
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Hijack().Close(ctx) }()
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended('download-global-rates:'||$1::text,0))", f.client.String()); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	jobState := "succeeded"
	if state == "abandoned" {
		jobState = "dead"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job(id,kind,state,payload,max_attempts,attempts,lease_owner,lease_token,lease_until,finished_at,created_at,updated_at)
 VALUES($1,'download_global_rates',$3,jsonb_build_object('client_id',$2::uuid,'config_stamp',pulse_global_rates_binding($2)),1,1,'retention-fixture',1,clock_timestamp()+interval '1 minute',clock_timestamp()-$4*interval '1 day',clock_timestamp()-$4*interval '1 day',clock_timestamp()-$4*interval '1 day')`, id, f.client, jobState, days); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO download_global_rate_attempt(job_id,client_id,config_stamp,state,execution_owner,execution_token,started_at,observed_at,finished_at,download_bytes_per_second,upload_bytes_per_second,failure_code)
 VALUES($1,$2,pulse_global_rates_binding($2),$3,'retention-fixture',1,clock_timestamp()-$4*interval '1 day'-interval '1 second',CASE WHEN $3='abandoned' THEN NULL ELSE clock_timestamp()-$4*interval '1 day' END,clock_timestamp()-$4*interval '1 day',CASE WHEN $3='succeeded' THEN 123 ELSE NULL END,CASE WHEN $3='succeeded' THEN 456 ELSE NULL END,CASE WHEN $3='succeeded' THEN '' WHEN $3='abandoned' THEN 'unavailable' ELSE 'timeout' END)`, id, f.client, state, days); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO download_global_rate_schedule(client_id,latest_job_id,last_good_job_id) VALUES($1,$2::uuid,CASE WHEN $3='succeeded' THEN $2::uuid ELSE NULL END)
 ON CONFLICT(client_id) DO UPDATE SET latest_job_id=$2::uuid,last_good_job_id=CASE WHEN $3='succeeded' THEN $2::uuid ELSE download_global_rate_schedule.last_good_job_id END`, f.client, id, state); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Expired terminal leases are not an authorization to perform another call.
	f.exec(t, "UPDATE job SET lease_until=clock_timestamp()-interval '1 day' WHERE id=$1", id)
	return id
}

func TestHistoryRetentionProtectedGraphAndRollbackPG(t *testing.T) {
	f := newGlobalRateFixture(t)
	good := seedRetentionObservation(t, f, "succeeded", 40)
	expired := seedRetentionObservation(t, f, "failed", 20)
	uncertain := seedRetentionObservation(t, f, "abandoned", 100)
	latest := seedRetentionObservation(t, f, "failed", 1)
	active := f.claim(t)
	store := postgres.NewHistoryRetentionStore(f.pool)
	// A failure after deleting the observation must roll the whole graph back.
	f.exec(t, `CREATE FUNCTION retention_fixture_fail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'owned cleanup failure';END$$`)
	f.exec(t, `CREATE TRIGGER retention_fixture_fail BEFORE DELETE ON job FOR EACH ROW EXECUTE FUNCTION retention_fixture_fail()`)
	if stats, err := store.PruneHistory(t.Context()); err == nil || stats.Observations != 0 {
		t.Fatal("partial cleanup", stats, err)
	}
	if n := f.count(t, "SELECT count(*) FROM download_global_rate_attempt WHERE job_id=$1", expired); n != 1 {
		t.Fatal("rollback lost observation")
	}
	f.exec(t, "DROP TRIGGER retention_fixture_fail ON job")
	f.exec(t, "DROP FUNCTION retention_fixture_fail()")
	// Concurrent cleanup skips locked work without blocking unrelated history.
	hold, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(t.Context()) }()
	if _, err = hold.Exec(t.Context(), "SELECT id FROM job WHERE id=$1 FOR UPDATE", expired); err != nil {
		t.Fatal(err)
	}
	if stats, err := store.PruneHistory(t.Context()); err != nil || stats.Observations != 0 {
		t.Fatal("locked cleanup", stats, err)
	}
	if err = hold.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	stats, err := store.PruneHistory(t.Context())
	if err != nil || stats.Observations != 1 || stats.Jobs != 1 {
		t.Fatal(stats, err)
	}
	for _, id := range []uuid.UUID{good, uncertain, latest, active.ID} {
		if f.count(t, "SELECT count(*) FROM download_global_rate_attempt WHERE job_id=$1", id) != 1 || f.count(t, "SELECT count(*) FROM job WHERE id=$1", id) != 1 {
			t.Fatal("protected history removed", id)
		}
	}
	if f.count(t, "SELECT count(*) FROM job WHERE id=$1", expired) != 0 {
		t.Fatal("orphan job")
	}
}

func TestHistoryRetentionCandidateBatchAndQueryEvidencePG(t *testing.T) {
	f := newGlobalRateFixture(t)
	site := interactiveSite(t, f.pool, "retention", "online", true, false)
	f.exec(t, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes,created_at)
 SELECT gen_random_uuid(),$1,'owned expired candidate','retention-'||n,decode('01','hex'),1,clock_timestamp()-interval '31 days' FROM generate_series(1,205)n`, site)
	protected, fresh, query := uuid.New(), uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes,created_at) VALUES($1,$3,'protected','protected',decode('01','hex'),1,clock_timestamp()-interval '31 days'),($2,$3,'fresh','fresh',decode('01','hex'),1,clock_timestamp())`, protected, fresh, site)
	f.exec(t, `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,results_count,finished_at) VALUES($1,$2,'search','proof',1,'succeeded',1,clock_timestamp())`, query, site)
	f.exec(t, "INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)", query, protected)
	store := postgres.NewHistoryRetentionStore(f.pool)
	stats, err := store.PruneHistory(t.Context())
	if err != nil || stats.Candidates != 200 {
		t.Fatal("first bounded batch", stats, err)
	}
	stats, err = store.PruneHistory(t.Context())
	if err != nil || stats.Candidates != 5 {
		t.Fatal("remaining batch", stats, err)
	}
	if f.count(t, "SELECT count(*) FROM release_candidate WHERE id=ANY($1::uuid[])", []uuid.UUID{protected, fresh}) != 2 {
		t.Fatal("current or proven candidate removed")
	}
	if f.count(t, "SELECT count(*) FROM indexer_query_candidate WHERE query_id=$1 AND candidate_id=$2", query, protected) != 1 {
		t.Fatal("query proof changed")
	}
}
