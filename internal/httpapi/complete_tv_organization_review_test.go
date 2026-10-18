package httpapi_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
)

func TestCompleteTVOrganizationPGFinalizeRechecksAllFacts(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	r, e := f.confirm("tail-facts")
	if e != nil {
		t.Fatal(e)
	}
	f.n.exec(t, `CREATE FUNCTION revoke_at_full_finalize() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.kind='download_organization' AND NEW.state='succeeded' AND OLD.state='running' THEN UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE source_file_id=(SELECT target_file_id FROM transfer_step WHERE plan_id=(SELECT transfer_plan_id FROM download_task WHERE id=(NEW.payload->>'task_id')::uuid) ORDER BY step_order LIMIT 1) AND stage='imported'; END IF; RETURN NEW; END$$`)
	f.n.exec(t, `CREATE TRIGGER revoke_at_full_finalize BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION revoke_at_full_finalize()`)
	_ = f.run(t, r) // The worker may acknowledge a safe manual-review transition; verify persisted facts below.
	var revoked, lanes int
	if e = f.n.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM unit_fact WHERE revoked_at IS NOT NULL),(SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL)`, f.command.OperationID).Scan(&revoked, &lanes); e != nil || revoked != 0 || lanes != 2 {
		t.Fatal("tail committed invalid facts or released protection", revoked, lanes, e)
	}
}

func TestCompleteTVOrganizationPGLegacyPlanDeletion(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	id := uuid.New()
	f.n.exec(t, `INSERT INTO transfer_plan(id,operation_id,root_id,state,strategy) SELECT $1,$2,root_id,'created','hardlink' FROM subscription WHERE id=$3`, id, uuid.New(), f.n.f.sub.Id)
	tag, e := f.n.f.a.DBPool().Exec(context.Background(), `DELETE FROM transfer_plan WHERE id=$1`, id)
	if e != nil || tag.RowsAffected() != 1 {
		t.Fatal("ordinary plan delete silently skipped", tag, e)
	}
}

func TestCompleteTVOrganizationPGPolicyLockTimeoutUnavailable(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	ctx := context.Background()
	tx, e := f.n.f.a.DBPool().Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `SELECT p.id FROM policy p JOIN subscription s ON s.quality_policy_id=p.id WHERE s.id=$1 FOR UPDATE OF p`, f.n.f.sub.Id); e != nil {
		t.Fatal(e)
	}
	if _, e = f.confirm("locked-policy"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("database lock failure became manual review", e)
	}
}

type delayedCompleteInspectFS struct{ ports.TransferFilesystem }
type delayedCompleteInspectRoot struct{ ports.TransferRoot }

func (f delayedCompleteInspectFS) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	roots, release, e := f.TransferFilesystem.LockRoots(ctx, paths...)
	if e != nil {
		return nil, nil, e
	}
	for path, r := range roots {
		roots[path] = delayedCompleteInspectRoot{r}
	}
	return roots, release, nil
}
func (r delayedCompleteInspectRoot) Inspect(ctx context.Context, rel string) (ports.FileIdentity, error) {
	if strings.Contains(filepath.Base(rel), "S01E02") {
		select {
		case <-time.After(5200 * time.Millisecond):
		case <-ctx.Done():
			return ports.FileIdentity{}, ctx.Err()
		}
	}
	return r.TransferRoot.Inspect(ctx, rel)
}
func TestCompleteTVOrganizationPGSlowObservationNotRenewed(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	pool := f.n.f.a.DBPool()
	gen := idgen.New()
	fs := delayedCompleteInspectFS{rootfs.NewFilesystem()}
	f.service = transfer.NewCompleteTVOrganizationService(transferstore.NewRepository(pool, gen, outbox.New(pool, gen, clock.New()), fs))
	if _, e := f.confirm("slow-observation"); !errors.Is(e, ports.ErrTransferReviewRequired) {
		t.Fatal("slow file observation received fresh timestamp", e)
	}
	var count int
	if e := pool.QueryRow(context.Background(), `SELECT count(*) FROM complete_tv_transfer_evidence`).Scan(&count); e != nil || count != 0 {
		t.Fatal("expired evidence left plan", count, e)
	}
}
func TestCompleteTVOrganizationPGCachedReceiptCurrentOwnership(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	if _, e := f.confirm("owner"); e != nil {
		t.Fatal(e)
	}
	other := uuid.New()
	f.n.exec(t, `INSERT INTO app_user(id,username,password_hash,role) SELECT $1::uuid,$1::uuid::text,password_hash,'admin' FROM app_user WHERE id=$2`, other, f.n.actor.ID)
	f.n.exec(t, `UPDATE subscription SET user_id=$1 WHERE id=$2`, other, f.n.f.sub.Id)
	if _, e := f.confirm("owner"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("cached receipt bypassed current ownership", e)
	}
}

func TestCompleteTVOrganizationPGEvidenceLockFailureStaysRetryable(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	if _, e := f.confirm("evidence-lock"); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	blocker, e := f.n.f.a.DBPool().Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, e = blocker.Exec(ctx, `LOCK TABLE complete_tv_transfer_evidence IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	tx, e := f.n.f.a.DBPool().Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `SET LOCAL lock_timeout='100ms'`); e != nil {
		t.Fatal(e)
	}
	_, e = postgres.ReadCompleteTVOrganizationInputs(ctx, db.New(tx), f.command.OperationID, uuid.Nil, false)
	if e == nil || errors.Is(e, ports.ErrTransferReviewRequired) {
		t.Fatal("evidence read converted database failure to review", e)
	}
}
