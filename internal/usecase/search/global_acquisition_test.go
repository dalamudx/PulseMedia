package search_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func globalCommandFixture(t *testing.T) (*roundFixture, *acquisition.Service, acquisition.AcquireMovieCommand, uuid.UUID) {
	t.Helper()
	f := runtimeFixture(t)
	ctx := context.Background()
	round := f.collect(t)
	candidates, e := f.q.ListRoundQueryCandidates(ctx, round.ID)
	if e != nil {
		t.Fatal(e)
	}
	var selected uuid.UUID
	for _, c := range candidates {
		if c.Guid == "correct" {
			selected = c.ID
		}
	}
	if selected == uuid.Nil {
		t.Fatal("known matching candidate missing")
	}
	w, e := f.q.GetWantedItemByID(ctx, f.wanted)
	if e != nil {
		t.Fatal(e)
	}
	rootB := uuid.New()
	if _, e = f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: rootB, Path: t.TempDir(), MediaType: "movie", Name: "Root B"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", rootB); e != nil {
		t.Fatal(e)
	}
	ids := idgen.New()
	svc := acquisition.NewService(postgres.NewAcquisitionStore(f.pool, ids, clock.New(), acquisition.SelectMovieRound))
	svc.SetRequirementRepository(postgres.NewDownloadSubmissionStore(f.pool, ids))
	return f, svc, acquisition.AcquireMovieCommand{UnitIDs: []uuid.UUID{w.UnitID}, RootID: f.root, CandidateID: selected, DownloaderID: &f.downloader}, rootB
}
func TestGlobalMovieLaneSharesOnlyCompatibleDemand(t *testing.T) {
	f, svc, cmd, rootB := globalCommandFixture(t)
	ctx := context.Background()
	a := pgtest.CreateUserActor(t, f.q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	b := pgtest.CreateUserActor(t, f.q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	first, e := svc.AcquireMovie(ctx, a, cmd)
	if e != nil {
		t.Fatal(e)
	}
	cmd.RootID = rootB
	second, e := svc.AcquireMovie(ctx, b, cmd)
	if e != nil || second.Status != "attached" || first.OperationID != second.OperationID || first.JobID != second.JobID {
		t.Fatal("compatible roots submitted twice", first, second, e)
	}
	var jobs, lanes, demands int
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM job WHERE kind='download_submission'),(SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL),(SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND state='pending')", first.OperationID).Scan(&jobs, &lanes, &demands); e != nil || jobs != 1 || lanes != 1 || demands != 2 {
		t.Fatal(jobs, lanes, demands, e)
	}
	// The original target may withdraw, but the physical source path is immutable
	// and another valid root's demand keeps a single submission authorized.
	if released, e := svc.ReleaseRequirement(ctx, a, first.OperationID, cmd.UnitIDs[0], f.root); e != nil || released {
		t.Fatal("first root withdrew last protection", released, e)
	}
	job, e := f.queue.Claim(ctx, []string{"download_submission"}, "global-owner", 5*time.Minute)
	if e != nil || job == nil {
		t.Fatal(job, e)
	}
	candidate, e := f.q.GetCandidateByID(ctx, first.SourceCandidateID)
	if e != nil {
		t.Fatal(e)
	}
	root, e := f.q.GetLibraryRootByID(ctx, f.root)
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := postgres.NewDownloadSubmissionStore(f.pool, idgen.New()).PrepareSubmission(ctx, ports.DownloadSubmissionCommand{Lease: ports.JobLease{ID: job.ID, Owner: "global-owner", Token: job.LeaseToken}, TaskID: uuid.New(), OperationID: first.OperationID, ClientID: f.downloader, CandidateID: candidate.ID, InfoHash: candidate.InfoHash.String, SavePath: root.Path, CipherHash: sha256.Sum256(candidate.LinkEncrypted)})
	if e != nil || prepared.Declined {
		t.Fatal("remaining root not authorized or source path altered", prepared, e)
	}
	task, e := f.q.GetDownloadTaskByOpID(ctx, first.OperationID)
	if e != nil || task.SavePath != root.Path {
		t.Fatal("new demand changed physical destination", task, e)
	}
}
func TestGlobalLaneRejectsUnknownAndIncompatibleQuality(t *testing.T) {
	for _, caseName := range []string{"unknown", "source-changed", "quality-rejected", "other-client"} {
		t.Run(caseName, func(t *testing.T) {
			f, svc, cmd, rootB := globalCommandFixture(t)
			ctx := context.Background()
			actor := pgtest.CreateUserActor(t, f.q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
			first, e := svc.AcquireMovie(ctx, actor, cmd)
			if e != nil {
				t.Fatal(e)
			}
			cmd.RootID = rootB
			switch caseName {
			case "unknown":
				_, e = f.q.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: uuid.New(), OperationID: first.OperationID, ClientID: f.downloader, CandidateID: cmd.CandidateID, State: "unknown", SavePath: "/fixture"})
			case "source-changed":
				_, e = f.pool.Exec(ctx, "UPDATE release_candidate SET title=title||'.Changed' WHERE id=$1", cmd.CandidateID)
			case "quality-rejected":
				rules := map[string]any{"resolution_order": map[string]int{"2160p": 500}, "source_order": map[string]int{"Bluray": 40}, "cutoff_score": 0, "reject_terms": []string{}, "reject_groups": []string{}}
				raw, _ := json.Marshal(rules)
				_, e = f.q.InsertPolicyRevision(ctx, db.InsertPolicyRevisionParams{ID: uuid.New(), PolicyID: "default", Revision: 2, Rules: raw})
				if e == nil {
					_, e = f.pool.Exec(ctx, "UPDATE policy SET current_revision=2 WHERE id='default'")
				}
			case "other-client":
				other := uuid.New()
				_, e = f.q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: other, Name: "different", ClientType: "qbittorrent", Url: "https://fixture.invalid", PasswordEncrypted: []byte("fixture"), RemotePathMap: []byte(`{}`)})
				cmd.DownloaderID = &other
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = svc.AcquireMovie(ctx, pgtest.CreateUserActor(t, f.q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}), cmd); !errors.Is(e, acquisition.ErrUnitAlreadyReserved) {
				t.Fatal("incompatible demand stole or split lane", caseName, e)
			}
			var jobs, demands int
			if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM job WHERE kind='download_submission'),(SELECT count(*) FROM download_requirement WHERE operation_id=$1)", first.OperationID).Scan(&jobs, &demands); e != nil || jobs != 1 || demands != 1 {
				t.Fatal(jobs, demands, e)
			}
		})
	}
}
func TestGlobalLaneConcurrentTwoRootsOneSubmission(t *testing.T) {
	f, svc, cmd, rootB := globalCommandFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan *acquisition.AcquireResult, 8)
	failures := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			c := cmd
			if i%2 == 1 {
				c.RootID = rootB
			}
			r, e := svc.AcquireMovie(ctx, pgtest.CreateUserActor(t, f.q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}), c)
			results <- r
			failures <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal("two roots lock ordering failed", e)
		}
	}
	var op uuid.UUID
	for r := range results {
		if op == uuid.Nil {
			op = r.OperationID
		}
		if r.OperationID != op {
			t.Fatal("physical operation duplicated across roots")
		}
	}
	var jobs, demands int
	if e := f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM job WHERE kind='download_submission'),(SELECT count(*) FROM download_requirement WHERE operation_id=$1)", op).Scan(&jobs, &demands); e != nil || jobs != 1 || demands != 8 {
		t.Fatal(jobs, demands, e)
	}
}
