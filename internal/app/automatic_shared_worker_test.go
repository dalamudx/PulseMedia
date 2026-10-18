package app

import (
	"context"
	"encoding/hex"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppRunAttachesCompatibleRootWithoutNewQueryOrDownload(t *testing.T) {
	f := organizationSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	targetB := filepath.Join(t.TempDir(), "B")
	if e := os.MkdirAll(targetB, 0750); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(targetB, ".pulsemedia_root"), []byte("root identity marker"), 0600); e != nil {
		t.Fatal(e)
	}
	rootB := uuid.New()
	if _, e := f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: rootB, Path: targetB, MediaType: "movie", Name: "B"}); e != nil {
		t.Fatal(e)
	}
	if _, e := library.NewScanner(f.gen, rootfs.NewFilesystem(), postgres.NewScanObservationStore(f.pool)).ScanRoot(ctx, rootB); e != nil {
		t.Fatal(e)
	}
	source, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
	if e != nil {
		t.Fatal(e)
	}
	candidate, e := f.q.GetCandidateByID(ctx, source.CandidateID)
	if e != nil {
		t.Fatal(e)
	}
	cipher, e := crypto.Encrypt(outboxTestKey, []byte("https://fixture.invalid/link"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE release_candidate SET source_config_version=1,size_bytes=18,quality_profile=$2,title='Movie.2020.1080p.BluRay',link_encrypted=$3 WHERE id=$1", candidate.ID, []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`), cipher); e != nil {
		t.Fatal(e)
	}
	candidate, e = f.q.GetCandidateByID(ctx, source.CandidateID)
	if e != nil {
		t.Fatal(e)
	}
	root, e := f.q.GetLibraryRootByID(ctx, f.root)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateAcquisitionLane(ctx, db.CreateAcquisitionLaneParams{ID: uuid.New(), UnitID: f.unit, OperationID: f.op, CandidateID: candidate.ID, SourceRootID: f.root, SourceGeneration: root.Generation, SourceStamp: quality.AcquisitionSourceStamp(candidate.Title, candidate.InfoHash.String, candidate.LinkEncrypted, candidate.QualityProfile)}); e != nil {
		t.Fatal(e)
	}
	if e = f.q.BindPhysicalAcquisitionTask(ctx, db.BindPhysicalAcquisitionTaskParams{OperationID: f.op, TaskID: pgtype.UUID{Bytes: f.taskID, Valid: true}}); e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{"operation_id":"` + f.op.String() + `","candidate_id":"` + candidate.ID.String() + `","root_id":"` + f.root.String() + `","root_generation":1,"client_id":"` + f.client.String() + `"}`)
	if _, e = f.q.EnqueueJob(ctx, db.EnqueueJobParams{ID: uuid.New(), Kind: "download_submission", Payload: raw, MaxAttempts: 3, NotBefore: pgtype.Timestamptz{Time: time.Now(), Valid: true}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE job SET state='succeeded' WHERE kind='download_submission' AND payload->>'operation_id'=$1", f.op.String()); e != nil {
		t.Fatal(e)
	}
	user := uuid.New()
	if _, e = f.q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "shared-B", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	eStore := postgres.NewSubscriptionIntentStore(f.pool, f.gen)
	sub, _, e := subscription.NewService(eStore, eStore, postgres.NewSubscriptionControlStore(f.pool, f.gen), eStore).CreateSubscription(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleMember}, subscription.CreateSubscriptionCommand{MediaID: f.media, RootID: rootB, ScopeType: "all", IndexerIDs: []uuid.UUID{candidate.IndexerID}, DownloaderID: &f.client})
	if e != nil {
		t.Fatal(e)
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	a, e := New(cfg, WithDBPool(f.pool), WithLogger(logging.NewLogger(io.Discard, "error")))
	if e != nil {
		t.Fatal(e)
	}
	runtime, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(runtime) }()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("shared runtime did not join")
		}
	}()
	defer func() {
		if t.Failed() {
			diag := context.Background()
			var body []byte
			e := f.pool.QueryRow(diag, "SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(jsonb_build_object('kind',kind,'state',state,'error',error)) FROM job),'rounds',(SELECT jsonb_agg(jsonb_build_object('state',state,'reason',abort_reason,'snapshot',decision_snapshot)) FROM search_round),'task',(SELECT jsonb_agg(jsonb_build_object('state',state,'organization',organization_state)) FROM download_task),'requirements',(SELECT jsonb_agg(jsonb_build_object('root',root_id,'state',state)) FROM download_requirement))").Scan(&body)
			t.Logf("shared runtime diagnostic: %s error=%v", body, e)
		}
	}()
	waitOutboxCondition(t, ctx, func() bool {
		var count int
		e := f.pool.QueryRow(ctx, "SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND root_id=$2 AND actor_id=$3", f.op, rootB, user.String()).Scan(&count)
		return e == nil && count == 1
	})
	var queries, downloads int
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM indexer_query),(SELECT count(*) FROM job WHERE kind='download_submission')").Scan(&queries, &downloads); e != nil || queries != 0 || downloads != 1 {
		t.Fatal("scheduled sharing started new remote effect", queries, downloads, e)
	}
	actual, e := f.q.GetSubscriptionByID(ctx, sub.ID)
	if e != nil || actual.State != "active" {
		t.Fatal(actual, e)
	}
}
