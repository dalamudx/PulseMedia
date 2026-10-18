package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestTVSelectionObservationPGCancellation(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		t.Run(fmt.Sprint(waiting), func(t *testing.T) {
			f, svc, _, r := selectionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !waiting {
				cancel()
				if _, err := svc.Preview(ctx, f.actor, r); !errors.Is(err, context.Canceled) {
					t.Fatalf("caller cancellation lost: %v", err)
				}
				return
			}
			pool := f.f.a.DBPool()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err = tx.Exec(ctx, "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", uuid.MustParse(f.actor.ID)); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := svc.Preview(ctx, f.actor, r); done <- err }()
			selectionWaitFor(t, func() bool {
				var blocked bool
				if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%app_user%')").Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				return blocked
			})
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("locked caller cancellation lost: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("canceled account waiter did not stop")
			}
		})
	}
}

func selectionWaitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected database barrier not observed")
}

func TestTVSelectionObservationPGExpiryAfterLastRead(t *testing.T) {
	for _, source := range []bool{false, true} {
		name := "projection"
		if source {
			name = "source"
		}
		t.Run(name, func(t *testing.T) { selectionExpiryAfterLastRead(t, source) })
	}
}

func selectionExpiryAfterLastRead(t *testing.T, source bool) {
	t.Helper()
	f, svc, _, r := selectionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	delayedID := f.candidate
	if source {
		// The first pack is checked before the second pack's final read blocks.
		// Both observations expire while blocked, so sequential checks are insufficient.
		delayedID = uuid.New()
		f.exec(t, `INSERT INTO release_candidate SELECT (jsonb_populate_record(NULL::release_candidate,to_jsonb(c)||jsonb_build_object('id',$1::uuid,'guid','expiry:'||$1::text))).* FROM release_candidate c WHERE c.id=$2`, delayedID, f.candidate)
		f.exec(t, "INSERT INTO indexer_query_candidate(query_id,candidate_id) SELECT query_id,$1 FROM indexer_query_candidate WHERE candidate_id=$2", delayedID, f.candidate)
		f.exec(t, "UPDATE indexer_query SET finished_at=clock_timestamp()-interval '30 minutes'+interval '2 seconds' WHERE indexer_id=$1", f.indexer)
		for i, id := range []uuid.UUID{f.candidate, delayedID} {
			original := f.candidate
			f.candidate = id
			v := f.preview(t)
			if !v.CanReview {
				t.Fatal(v.HoldReason)
			}
			review, err := f.svc.Review(ctx, f.actor, f.coverageFixture.command(v), fmt.Sprintf("expiry-source-%d", i))
			f.candidate = original
			if err != nil {
				t.Fatal(err)
			}
			ref := ports.TVSelectionCandidateRef{CandidateID: id, ReviewID: review.ID}
			if i == 0 {
				r.Packs[0] = ref
			} else {
				r.Packs = append(r.Packs, ref)
			}
		}
		if p, err := svc.Preview(ctx, f.actor, r); err != nil || p.Decision.Kind != "pack" {
			t.Fatalf("source fixture not initially executable as a preview: %v %v", p, err)
		}
	}
	pool := f.f.a.DBPool()
	gate, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Release()
	const lockKey = 62031001
	if _, err := gate.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = gate.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockKey) }()
	// A transparent view blocks a real final candidate read, preserving all rows.
	delayedRead := 4
	if source {
		// Each pack performs three initial and two final candidate reads.
		// Delay the second pack only after the first pack's final reads finish.
		delayedRead = 9
	}
	f.exec(t, fmt.Sprintf(`ALTER TABLE release_candidate RENAME TO selection_candidate_base;
 CREATE SEQUENCE selection_candidate_reads;
 CREATE FUNCTION selection_delay_candidate(r selection_candidate_base) RETURNS selection_candidate_base LANGUAGE plpgsql VOLATILE AS $$
 BEGIN
  IF r.id='%s'::uuid AND position('GetCandidateByID' in current_query())>0 THEN
   IF nextval('selection_candidate_reads')=%d THEN
    PERFORM pg_advisory_lock(%d); PERFORM pg_advisory_unlock(%d);
   END IF;
  END IF;
  RETURN r;
 END $$;
 CREATE VIEW release_candidate AS SELECT (delayed.value).* FROM selection_candidate_base c
 CROSS JOIN LATERAL (SELECT selection_delay_candidate(c) AS value OFFSET 0) delayed;`, delayedID, delayedRead, lockKey, lockKey))
	if !source {
		f.exec(t, "UPDATE wanted_item SET projection_valid_until=clock_timestamp()+interval '2 seconds' WHERE subscription_id=$1", f.f.sub.Id)
	}
	before := selectionState(t, f)
	type outcome struct {
		kind string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		p, err := svc.Preview(ctx, f.actor, r)
		got := outcome{err: err}
		if p != nil {
			got.kind = p.Decision.Kind
		}
		done <- got
	}()
	selectionWaitFor(t, func() bool {
		var blocked bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%GetCandidateByID%')").Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	selectionWaitFor(t, func() bool {
		var expired bool
		query := "SELECT bool_and(projection_valid_until<=clock_timestamp()) FROM wanted_item WHERE subscription_id=$1"
		id := f.f.sub.Id
		if source {
			query = "SELECT bool_and(finished_at+interval '30 minutes'<=clock_timestamp()) FROM indexer_query WHERE indexer_id=$1"
			id = f.indexer
		}
		if err := pool.QueryRow(ctx, query, id).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		return expired
	})
	if _, err := gate.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if source {
			if got.err != nil && !errors.Is(got.err, ports.ErrTVSelectionChanged) {
				t.Fatal(got.err)
			}
			if got.err == nil && got.kind != "hold" {
				t.Fatalf("earlier candidate's expired source accepted: %s", got.kind)
			}
		} else if !errors.Is(got.err, ports.ErrTVSelectionChanged) {
			t.Fatalf("late expired projection accepted: %v", got.err)
		}
	case <-ctx.Done():
		t.Fatal("late candidate reader did not stop")
	}
	if before != selectionState(t, f) {
		t.Fatal("failed observation changed persistent business state")
	}
}
