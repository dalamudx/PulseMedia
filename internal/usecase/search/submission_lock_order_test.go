package search_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"runtime"
	"testing"
	"time"
)

func TestSubmissionLockOrderBeforeMediaAndCandidate(t *testing.T) {
	for _, blocked := range []string{"operation", "indexer"} {
		t.Run(blocked, func(t *testing.T) {
			f := runtimeFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			round := f.collect(t)
			decisionJob := f.finalJob(t, round)
			r, e := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(decisionJob))
			if e != nil || r.OperationID == uuid.Nil {
				t.Fatal(r, e)
			}
			j, e := f.queue.Claim(ctx, []string{"download_submission"}, "lock-order", 5*time.Minute)
			if e != nil || j == nil {
				t.Fatal(j, e)
			}
			var payload struct {
				CandidateID uuid.UUID `json:"candidate_id"`
			}
			if e = json.Unmarshal(j.Payload, &payload); e != nil {
				t.Fatal(e)
			}
			cand, e := f.q.GetCandidateByID(ctx, payload.CandidateID)
			if e != nil {
				t.Fatal(e)
			}
			root, e := f.q.GetLibraryRootByID(ctx, f.root)
			if e != nil {
				t.Fatal(e)
			}
			var mediaID uuid.UUID
			if e = f.pool.QueryRow(ctx, "SELECT u.media_id FROM media_unit u JOIN wanted_item w ON w.unit_id=u.id WHERE w.id=$1", f.wanted).Scan(&mediaID); e != nil {
				t.Fatal(e)
			}
			hold, e := f.pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = hold.Rollback(context.WithoutCancel(ctx)) }()
			if blocked == "operation" {
				_, e = hold.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('acquisition:'||$1::uuid::text,0))", r.OperationID)
			} else {
				_, e = hold.Exec(ctx, "SELECT id FROM indexer WHERE id=$1 FOR UPDATE", cand.IndexerID)
			}
			if e != nil {
				t.Fatal(e)
			}
			cmd := ports.DownloadSubmissionCommand{Lease: ports.JobLease{ID: j.ID, Owner: "lock-order", Token: j.LeaseToken}, TaskID: uuid.New(), OperationID: r.OperationID, ClientID: f.downloader, CandidateID: cand.ID, InfoHash: cand.InfoHash.String, SavePath: root.Path, CipherHash: sha256.Sum256(cand.LinkEncrypted)}
			done := make(chan error, 1)
			go func() {
				_, e := postgres.NewDownloadSubmissionStore(f.pool, idgen.New()).PrepareSubmission(ctx, cmd)
				done <- e
			}()
			defer func() {
				cancel()
				_ = hold.Rollback(context.Background())
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("worker did not exit")
				}
			}()
			pattern := "%acquisition:%"
			if blocked == "indexer" {
				pattern = "%FROM indexer%"
			}
			for {
				var waiting bool
				e = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)", pattern).Scan(&waiting)
				if e != nil {
					t.Fatal(e)
				}
				if waiting {
					break
				}
				if ctx.Err() != nil {
					t.Fatal("did not reach blocked lock", ctx.Err())
				}
				runtime.Gosched()
			}
			probe, e := f.pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = probe.Rollback(context.WithoutCancel(ctx)) }()
			if blocked == "operation" {
				_, e = probe.Exec(ctx, "SELECT id FROM media WHERE id=$1 FOR NO KEY UPDATE NOWAIT", mediaID)
			} else {
				_, e = probe.Exec(ctx, "SELECT id FROM release_candidate WHERE id=$1 FOR UPDATE NOWAIT", cand.ID)
			}
			if e != nil {
				t.Fatal("submission holds later lock before blocked earlier lock", blocked, e)
			}
			_ = probe.Rollback(ctx)
			if e = hold.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			select {
			case e = <-done:
				if e != nil {
					t.Fatal(e)
				}
				done <- nil
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
