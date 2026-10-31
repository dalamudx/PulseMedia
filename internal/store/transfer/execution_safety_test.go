package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	transfercase "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type safetyFixture struct {
	svc                        *Service
	pool                       *pgxpool.Pool
	q                          *db.Queries
	root, unit                 uuid.UUID
	source, target, sourcePath string
}

func safetySetup(t *testing.T) *safetyFixture {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	gen := idgen.New()
	q := db.New(pool)
	f := &safetyFixture{pool: pool, q: q, root: uuid.New(), unit: uuid.New(), source: t.TempDir(), target: t.TempDir()}
	f.svc = NewService(pool, gen, outbox.New(pool, gen, clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
	// Failure injection needs actual configured recipients, never fixed generic delivery labels.
	serverKey, err := crypto.Encrypt(make([]byte, 32), []byte("fixture-server-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMediaServer(ctx, db.CreateMediaServerParams{ID: uuid.New(), Name: "Configured server", ServerType: "jellyfin", Url: "http://example.test", ApiKeyEncrypted: serverKey, PathMapping: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	channelKey, err := crypto.Encrypt(make([]byte, 32), []byte(`{"url":"https://example.test","secret":"fixture-signing-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: uuid.New(), Name: "Configured webhook", ChannelType: "webhook", ConfigEncrypted: channelKey}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.target, ".pulsemedia_root"), []byte("fixture root"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: f.root, Path: f.target, MediaType: "movie", Name: "Movies"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: f.source, MediaType: "movie", Name: "Download source"}); err != nil {
		t.Fatal(err)
	}
	media := uuid.New()
	if _, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Movie", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: f.unit, MediaID: media, UnitKey: "movie"}); err != nil {
		t.Fatal(err)
	}
	f.sourcePath = filepath.Join(f.source, "movie.mkv")
	if err := os.WriteFile(f.sourcePath, []byte("frozen video bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *safetyFixture) plan(t *testing.T) (db.TransferPlan, db.TransferStep) {
	t.Helper()
	plan, step, err := f.svc.CreateMoviePlan(context.Background(), uuid.New(), f.root, f.unit, f.sourcePath, "Movie", 2020, quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray})
	if err != nil {
		t.Fatal(err)
	}
	return *plan, *step
}
func (f *safetyFixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *safetyFixture) noImport(t *testing.T) {
	t.Helper()
	for _, table := range []string{"library_file", "library_file_unit", "unit_fact", "outbox_event", "outbox_delivery", "audit_log", "notification_batch", "notification_batch_member"} {
		if n := f.count(t, table); n != 0 {
			t.Fatalf("partial write in %s: %d", table, n)
		}
	}
	root, err := f.q.GetLibraryRootByID(context.Background(), f.root)
	if err != nil || root.MutationSeq != 0 {
		t.Fatalf("mutation sequence advanced: %+v %v", root, err)
	}
}
func (f *safetyFixture) oneImport(t *testing.T, planID uuid.UUID) {
	t.Helper()
	for _, table := range []string{"library_file", "library_file_unit", "unit_fact", "outbox_event", "audit_log"} {
		if n := f.count(t, table); n != 1 {
			t.Fatalf("expected exactly one %s, got %d", table, n)
		}
	}
	if n := f.count(t, "outbox_delivery"); n != 1 {
		t.Fatal("server delivery missing", n)
	}
	if f.count(t, "notification_batch") != 1 || f.count(t, "notification_batch_member") != 1 {
		t.Fatal("notification recipient missing from importing UoW")
	}
	plan, err := f.q.GetTransferPlanByID(context.Background(), planID)
	if err != nil || plan.State != "completed" {
		t.Fatalf("plan not completed: %v %v", plan.State, err)
	}
	var kind string
	if err := f.pool.QueryRow(context.Background(), "SELECT jsonb_typeof(payload) FROM outbox_event").Scan(&kind); err != nil || kind != "object" {
		t.Fatalf("event cannot be consumed as JSON: %s %v", kind, err)
	}
	facts, err := f.q.ListActiveFactsForUnit(context.Background(), f.unit)
	if err != nil || len(facts) != 1 {
		t.Fatal(err)
	}
	var profile quality.Profile
	if err := json.Unmarshal(facts[0].QualityProfile, &profile); err != nil || profile.Resolution != quality.Res1080p {
		t.Fatalf("quality lost: %s %v", facts[0].QualityProfile, err)
	}
}

func TestPlanCreationAtomicAndSourceWhitelisted(t *testing.T) {
	f := safetySetup(t)
	ctx := context.Background()
	outside := filepath.Join(t.TempDir(), "private.mkv")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.CreateMoviePlan(ctx, uuid.New(), f.root, f.unit, outside, "Movie", 2020, quality.Profile{}); !errors.Is(err, rootfs.ErrEscape) {
		t.Fatalf("unconfigured source accepted: %v", err)
	}
	for _, protected := range []string{".env", ".secrets/movie.mkv", "notes.txt"} {
		if _, _, err := f.svc.CreateMoviePlan(ctx, uuid.New(), f.root, f.unit, filepath.Join(f.source, protected), "Movie", 2020, quality.Profile{}); !errors.Is(err, ports.ErrFileEscape) {
			t.Fatalf("protected/non-media source was opened: %s %v", protected, err)
		}
	}
	if _, _, err := f.svc.CreateMoviePlan(ctx, uuid.New(), f.root, uuid.New(), f.sourcePath, "Movie", 2020, quality.Profile{}); err == nil {
		t.Fatal("invalid unit accepted")
	}
	if f.count(t, "transfer_plan") != 0 || f.count(t, "transfer_step") != 0 {
		t.Fatal("failed step left executable plan")
	}
}
func TestFrozenSourceIdentityAndTargetParentSymlink(t *testing.T) {
	for _, attack := range []string{"source_replaced", "source_parent_symlink", "target_parent_symlink", "target_root_replaced", "marker_missing"} {
		t.Run(attack, func(t *testing.T) {
			f := safetySetup(t)
			plan, step := f.plan(t)
			outside := t.TempDir()
			switch attack {
			case "source_replaced":
				if err := os.Rename(f.sourcePath, f.sourcePath+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.sourcePath, []byte("frozen video bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "source_parent_symlink":
				if err := os.Rename(f.source, f.source+".old"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.RemoveAll(f.source + ".old"); err != nil {
						t.Errorf("fixture cleanup: %v", err)
					}
				})
				if err := os.Symlink(f.source+".old", f.source); err != nil {
					t.Fatal(err)
				}
			case "target_parent_symlink":
				if err := os.Symlink(outside, filepath.Dir(step.TargetPath)); err != nil {
					t.Fatal(err)
				}
			case "marker_missing":
				if err := os.Remove(filepath.Join(f.target, ".pulsemedia_root")); err != nil {
					t.Fatal(err)
				}
			case "target_root_replaced":
				if err := os.Rename(f.target, f.target+".old"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.RemoveAll(f.target + ".old"); err != nil {
						t.Errorf("fixture cleanup: %v", err)
					}
				})
				if err := os.Mkdir(f.target, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.svc.ExecutePlan(context.Background(), plan.ID); err == nil {
				t.Fatal("frozen evidence was bypassed")
			}
			f.noImport(t)
			if files, err := os.ReadDir(outside); err != nil || len(files) > 0 {
				t.Fatalf("escape wrote outside root: %v %v", files, err)
			}
		})
	}
}
func TestConcurrentPlanExecutionAcrossServicesIsIdempotent(t *testing.T) {
	f := safetySetup(t)
	plan, _ := f.plan(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gen := idgen.New()
			svc := NewService(f.pool, gen, outbox.New(f.pool, gen, clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
			errs <- svc.ExecutePlan(context.Background(), plan.ID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent execution: %v", err)
		}
	}
	f.oneImport(t, plan.ID)
}
func TestStaleWriteRejectedAndPhysicalSlotRetained(t *testing.T) {
	f := safetySetup(t)
	ctx := context.Background()
	plan, step := f.plan(t)
	files := rootfs.NewFilesystem()
	roots, release, err := files.LockRoots(ctx, f.source, f.target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	claimed, err := f.q.ClaimTransferPlan(ctx, db.ClaimTransferPlanParams{ID: plan.ID, Owner: text("old-worker")})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.startStep(ctx, claimed, step); err != nil {
		t.Fatal(err)
	}
	if err := files.Hardlink(ctx, roots[f.source], step.SourceRel.String, roots[f.target], step.TargetRel.String, frozen(step)); err != nil {
		t.Fatal(err)
	}
	// Fencing must reject both a wrong owner and a wrong token, even during a live lease.
	wrong := claimed
	wrong.ExecutionOwner = text("other-worker")
	if err := f.svc.commitStep(ctx, wrong, step, f.observationTime(t)); !errors.Is(err, ErrExecutionRejected) {
		t.Fatalf("wrong owner committed: %v", err)
	}
	wrong = claimed
	wrong.ExecutionToken++
	if err := f.svc.commitStep(ctx, wrong, step, f.observationTime(t)); !errors.Is(err, ErrExecutionRejected) {
		t.Fatalf("wrong token committed: %v", err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE transfer_plan SET state='failed' WHERE id=$1", plan.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.commitStep(ctx, claimed, step, f.observationTime(t)); !errors.Is(err, ErrExecutionRejected) {
		t.Fatalf("inactive execution state committed: %v", err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE transfer_plan SET state='executing', lease_until=statement_timestamp()-interval '1 second' WHERE id=$1", plan.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.commitStep(ctx, claimed, step, f.observationTime(t)); !errors.Is(err, ErrExecutionRejected) {
		t.Fatalf("expired lease committed: %v", err)
	}
	if _, err := f.q.FinishTransferPlan(ctx, db.FinishTransferPlanParams{ID: plan.ID, Owner: claimed.ExecutionOwner.String, Token: claimed.ExecutionToken}); err == nil {
		t.Fatal("expired plan reported success")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	err = f.svc.ExecutePlan(waitCtx, plan.ID)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("new writer took expired physical slot: %v", err)
	}
	f.noImport(t)
	release()
	release = nil
	if err := f.svc.ExecutePlan(ctx, plan.ID); err != nil {
		t.Fatalf("observation recovery: %v", err)
	}
	f.oneImport(t, plan.ID)
}
func TestDatabaseFailureRollsBackEveryImportWriteAndRecovers(t *testing.T) {
	for _, table := range []string{"library_file_unit", "outbox_delivery", "audit_log", "notification_batch", "notification_batch_member"} {
		t.Run(table, func(t *testing.T) {
			f := safetySetup(t)
			ctx := context.Background()
			plan, step := f.plan(t)
			_, err := f.pool.Exec(ctx, "CREATE FUNCTION reject_transfer_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected transfer write failure'; END $$; CREATE TRIGGER reject_transfer_write BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION reject_transfer_write();")
			if err != nil {
				t.Fatal(err)
			}
			if err := f.svc.ExecutePlan(ctx, plan.ID); err == nil {
				t.Fatal("failed UoW reported success")
			}
			f.noImport(t)
			if _, err := os.Stat(step.TargetPath); err != nil {
				t.Fatalf("physical result not available for observation: %v", err)
			}
			if _, err := f.pool.Exec(ctx, "DROP TRIGGER reject_transfer_write ON "+table); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, "UPDATE transfer_plan SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1", plan.ID); err != nil {
				t.Fatal(err)
			}
			gen := idgen.New()
			restarted := NewService(f.pool, gen, outbox.New(f.pool, gen, clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
			if err := restarted.ExecutePlan(ctx, plan.ID); err != nil {
				t.Fatalf("restart did not observe exact link: %v", err)
			}
			f.oneImport(t, plan.ID)
		})
	}
}
