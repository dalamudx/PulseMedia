package search_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func TestPrepareAndFinalRequirementReleaseLinearize(t *testing.T) {
	for attempt := 0; attempt < 6; attempt++ {
		t.Run(uuid.NewString(), func(t *testing.T) {
			f := runtimeFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			round := f.collect(t)
			job := f.finalJob(t, round)
			decision, e := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
			if e != nil {
				t.Fatal(e)
			}
			submission, e := f.queue.Claim(ctx, []string{"download_submission"}, "race-owner", 5*time.Minute)
			if e != nil || submission == nil {
				t.Fatal(submission, e)
			}
			var payload struct {
				CandidateID uuid.UUID `json:"candidate_id"`
			}
			if json.Unmarshal(submission.Payload, &payload) != nil {
				t.Fatal("payload")
			}
			candidate, e := f.q.GetCandidateByID(ctx, payload.CandidateID)
			if e != nil {
				t.Fatal(e)
			}
			root, e := f.q.GetLibraryRootByID(ctx, f.root)
			if e != nil {
				t.Fatal(e)
			}
			rows, e := f.q.ListRequirementsForOperation(ctx, decision.OperationID)
			if e != nil {
				t.Fatal(e)
			}
			r := rows[0]
			repo := postgres.NewDownloadSubmissionStore(f.pool, idgen.New())
			command := ports.DownloadSubmissionCommand{Lease: ports.JobLease{ID: submission.ID, Owner: "race-owner", Token: submission.LeaseToken}, TaskID: uuid.New(), OperationID: decision.OperationID, ClientID: f.downloader, CandidateID: candidate.ID, InfoHash: candidate.InfoHash.String, SavePath: root.Path, CipherHash: sha256.Sum256(candidate.LinkEncrypted)}
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: r.ActorID, Role: f.actor.Role}
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			var releaseError error
			var prepared *ports.DownloadSubmissionReceipt
			var prepareError error
			go func() { defer wg.Done(); <-start; prepared, prepareError = repo.PrepareSubmission(ctx, command) }()
			go func() {
				defer wg.Done()
				<-start
				_, releaseError = repo.ReleaseRequirement(ctx, actor, decision.OperationID, r.UnitID, r.RootID)
			}()
			close(start)
			wg.Wait()
			if releaseError != nil {
				t.Fatal("release failed instead of serializing", releaseError)
			}
			var tasks, protected int
			if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM download_task WHERE operation_id=$1),(SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND state IN ('reserved','dispatched'))", decision.OperationID).Scan(&tasks, &protected); e != nil {
				t.Fatal(e)
			}
			switch tasks {
			case 1:
				if protected != 1 || prepareError != nil || prepared == nil {
					t.Fatal("prepared physical task lost protection", tasks, protected, prepareError)
				}
			case 0:
				if protected != 0 || prepareError == nil {
					t.Fatal("unsent release still authorized network", tasks, protected, prepareError)
				}
			default:
				t.Fatal("two physical tasks", tasks)
			}
		})
	}
}
