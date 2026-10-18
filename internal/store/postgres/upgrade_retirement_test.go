package postgres_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type retirementFS79 struct {
	ports.RecycleFilesystem
	calls atomic.Int32
}

func (f *retirementFS79) Recycle(ctx context.Context, root ports.TransferRoot, old, target string, id ports.FileIdentity) error {
	f.calls.Add(1)
	return f.RecycleFilesystem.Recycle(ctx, root, old, target, id)
}
func retirementFixture79(t *testing.T) (*organizationJobFixture, *postgres.MovieUpgradeRetirementStore, *retirementFS79, uuid.UUID) {
	t.Helper()
	f, lease, _, ingestion := ingestionFixture78(t)
	if out, e := ingestion.IngestMovieUpgradeOrganization(context.Background(), f.actor, lease.ID); e != nil || out == nil {
		t.Fatal(out, e)
	}
	filesystem := &retirementFS79{RecycleFilesystem: rootfs.NewFilesystem()}
	repo, e := postgres.NewMovieUpgradeRetirementStore(f.trackingFixture.preparation, filesystem)
	if e != nil {
		t.Fatal(e)
	}
	var sub uuid.UUID
	if e = f.p.QueryRow(context.Background(), `SELECT subscription_id FROM movie_upgrade_authority WHERE operation_id=$1`, f.operation).Scan(&sub); e != nil {
		t.Fatal(e)
	}
	return f, repo, filesystem, sub
}
func confirmRetirement79(t *testing.T, f *organizationJobFixture, r *postgres.MovieUpgradeRetirementStore, sub uuid.UUID) (ports.ConfirmMovieUpgradeRetirement, *ports.MovieUpgradeRetirementReceipt) {
	t.Helper()
	preview, e := r.ReadMovieUpgradeRetirementPreview(context.Background(), f.actor, sub, f.operation)
	if e != nil || preview == nil {
		t.Fatal("preview", preview, e)
	}
	input := ports.ConfirmMovieUpgradeRetirement{OperationID: f.operation, InputStamp: preview.InputStamp, Confirmed: true}
	out, e := r.ConfirmMovieUpgradeRetirement(context.Background(), f.actor, sub, "retire-79", input)
	if e != nil || out == nil {
		t.Fatal("confirm", out, e)
	}
	return input, out
}
func retirementBusiness79(t *testing.T, f *organizationJobFixture) string {
	t.Helper()
	var raw string
	if e := f.p.QueryRow(context.Background(), `SELECT (pulse_upgrade_retirement_state($1)-'audits'-'events'-'receipts')::text`, f.operation).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	return raw
}
func retirementStatus79(t *testing.T, f *organizationJobFixture) string {
	t.Helper()
	var status string
	if e := f.p.QueryRow(context.Background(), `SELECT status FROM movie_upgrade_retirement_result WHERE operation_id=$1`, f.operation).Scan(&status); e != nil {
		t.Fatal(e)
	}
	return status
}

func TestMovieUpgradeRetirementNativePG(t *testing.T) {
	f, r, files, sub := retirementFixture79(t)
	ctx := context.Background()
	var oldRel string
	if e := f.p.QueryRow(ctx, `SELECT f.rel_path FROM movie_upgrade_authority a JOIN library_file f ON f.id=a.old_file_id WHERE a.operation_id=$1`, f.operation).Scan(&oldRel); e != nil {
		t.Fatal(e)
	}
	oldPath := filepath.Join(f.rootPath, oldRel)
	oldBefore, e := os.Stat(oldPath)
	if e != nil {
		t.Fatal(e)
	}
	seed := filepath.Join(f.rootPath, "kept-seeding-link.mkv")
	if e = os.Link(oldPath, seed); e != nil {
		t.Fatal(e)
	}
	input, out := confirmRetirement79(t, f, r, sub)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, e := r.ConfirmMovieUpgradeRetirement(ctx, f.actor, sub, "retire-79", input)
			if e != nil || got == nil || *got != *out {
				t.Error("unstable confirmation", got, e)
			}
		}()
	}
	wg.Wait()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := r.ExecuteMovieUpgradeRetirement(ctx, f.actor, f.operation); e != nil {
				t.Error("execute", e)
			}
		}()
	}
	wg.Wait()
	if files.calls.Load() != 1 || retirementStatus79(t, f) != "retired" {
		t.Fatal("not exactly one successful retirement", files.calls.Load())
	}
	if _, e = os.Stat(oldPath); !errors.Is(e, fs.ErrNotExist) {
		t.Fatal("old library path remains", e)
	}
	recycled, e := os.Stat(filepath.Join(f.rootPath, ".pulsemedia_recycle", f.operation.String(), oldRel))
	if e != nil || !os.SameFile(oldBefore, recycled) {
		t.Fatal("wrong recycled identity", e)
	}
	seeding, e := os.Stat(seed)
	if e != nil || !os.SameFile(oldBefore, seeding) {
		t.Fatal("seeding source changed", e)
	}
	source, e := os.Stat(f.sourcePath)
	if e != nil {
		t.Fatal(e)
	}
	target, e := os.Stat(executionTarget(t, f))
	if e != nil || !os.SameFile(source, target) {
		t.Fatal("new hardlink changed", e)
	}
	var valid bool
	e = f.p.QueryRow(ctx, `SELECT oldfile.retired_at=r.recorded_at AND oldfact.revoked_at=r.recorded_at AND oldfact.revocation_reason='quality_upgrade' AND newfile.retired_at IS NULL AND newfact.revoked_at IS NULL AND lane.released_at=r.recorded_at AND reservation.state='consumed' AND requirement.state='imported' AND r.retained_until=r.recorded_at+interval '7 days' AND oldmap.retired_at IS NULL AND newmap.retired_at IS NULL FROM movie_upgrade_authority a JOIN movie_upgrade_organization_ingestion i USING(operation_id) JOIN movie_upgrade_retirement_result r USING(operation_id) JOIN library_file oldfile ON oldfile.id=a.old_file_id JOIN library_file newfile ON newfile.id=i.file_id JOIN unit_fact oldfact ON oldfact.id=a.old_fact_id JOIN unit_fact newfact ON newfact.id=i.fact_id JOIN acquisition_reservation lane ON lane.operation_id=a.operation_id JOIN unit_reservation reservation ON reservation.operation_id=a.operation_id JOIN download_requirement requirement ON requirement.operation_id=a.operation_id JOIN library_file_unit oldmap ON oldmap.file_id=a.old_file_id JOIN library_file_unit newmap ON newmap.file_id=i.file_id WHERE a.operation_id=$1`, f.operation).Scan(&valid)
	if e != nil || !valid {
		t.Fatal("retirement effects", valid, e)
	}
	f.exec(t, `UPDATE subscription SET state='paused' WHERE id=$1`, sub)
	got, e := r.ReadMovieUpgradeRetirementReceipt(ctx, f.actor, sub, f.operation, "retire-79")
	if e != nil || got == nil || *got != *out {
		t.Fatal("historical receipt", got, e)
	}
	changed := input
	changed.InputStamp = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if got, e = r.ConfirmMovieUpgradeRetirement(ctx, f.actor, sub, "retire-79", changed); e == nil || got != nil {
		t.Fatal("changed request accepted", got, e)
	}
	token := *f.actor
	token.Scopes = []string{"read"}
	if got, e = r.ReadMovieUpgradeRetirementReceipt(ctx, &token, sub, f.operation, "retire-79"); e == nil || got != nil {
		t.Fatal("API credential read command receipt", got, e)
	}
}

func TestMovieUpgradeRetirementChangedFilePG(t *testing.T) {
	f, r, files, sub := retirementFixture79(t)
	confirmRetirement79(t, f, r, sub)
	before := retirementBusiness79(t, f)
	path := executionTarget(t, f)
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte("replacement"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := r.ExecuteMovieUpgradeRetirement(context.Background(), f.actor, f.operation); e != nil {
		t.Fatal(e)
	}
	if files.calls.Load() != 0 || retirementStatus79(t, f) != "manual_review" || retirementBusiness79(t, f) != before {
		t.Fatal("changed file caused retirement")
	}
}
func TestMovieUpgradeRetirementSharedOldFilePG(t *testing.T) {
	f, r, files, sub := retirementFixture79(t)
	f.exec(t, `WITH u AS(INSERT INTO media_unit(id,media_id,unit_key) SELECT gen_random_uuid(),u.media_id,'foreign-retirement-unit' FROM movie_upgrade_authority a JOIN media_unit u ON u.id=a.unit_id WHERE a.operation_id=$1 RETURNING id) INSERT INTO library_file_unit(file_id,unit_id) SELECT a.old_file_id,u.id FROM movie_upgrade_authority a,u WHERE a.operation_id=$1`, f.operation)
	if out, e := r.ReadMovieUpgradeRetirementPreview(context.Background(), f.actor, sub, f.operation); e == nil || out != nil {
		t.Fatal("shared old file allowed", out, e)
	}
	if files.calls.Load() != 0 {
		t.Fatal("physical call without confirmation")
	}
}
func TestMovieUpgradeRetirementOrphanPermitPG(t *testing.T) {
	f, r, files, sub := retirementFixture79(t)
	confirmRetirement79(t, f, r, sub)
	before := retirementBusiness79(t, f)
	f.exec(t, `WITH n AS(SELECT clock_timestamp() t) INSERT INTO movie_upgrade_retirement_permit SELECT $1,gen_random_uuid(),gen_random_uuid(),1,t,t+interval '150 milliseconds' FROM n`, f.operation)
	timer := time.NewTimer(180 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	pending, e := r.PendingMovieUpgradeRetirements(context.Background(), 25)
	if e != nil || len(pending) != 1 {
		t.Fatal("lost orphan", pending, e)
	}
	if e = r.ExecuteMovieUpgradeRetirement(context.Background(), f.actor, f.operation); e != nil {
		t.Fatal(e)
	}
	if files.calls.Load() != 0 || retirementStatus79(t, f) != "manual_review" || retirementBusiness79(t, f) != before {
		t.Fatal("orphan replayed or released protection")
	}
}
func TestMovieUpgradeRetirementTailRollbackPG(t *testing.T) {
	f, r, files, sub := retirementFixture79(t)
	confirmRetirement79(t, f, r, sub)
	before := retirementBusiness79(t, f)
	f.exec(t, `CREATE FUNCTION retire79_tail() RETURNS TRIGGER LANGUAGE plpgsql AS $$BEGIN IF NEW.action='upgrade.retirement-result' THEN UPDATE app_user SET is_active=false WHERE id=NEW.actor_id::uuid;END IF;RETURN NEW;END $$;CREATE TRIGGER retire79_tail AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION retire79_tail()`)
	if e := r.ExecuteMovieUpgradeRetirement(context.Background(), f.actor, f.operation); e == nil {
		t.Fatal("tail mutation accepted")
	}
	if files.calls.Load() != 1 || retirementBusiness79(t, f) != before {
		t.Fatal("tail partially committed")
	}
	var n int
	if e := f.p.QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_retirement_result`).Scan(&n); e != nil || n != 0 {
		t.Fatal("failed terminal record", n, e)
	}
	f.exec(t, `DROP TRIGGER retire79_tail ON audit_log;DROP FUNCTION retire79_tail()`)
	if e := r.ExecuteMovieUpgradeRetirement(context.Background(), f.actor, f.operation); e != nil {
		t.Fatal(e)
	}
	if files.calls.Load() != 1 || retirementStatus79(t, f) != "manual_review" || retirementBusiness79(t, f) != before {
		t.Fatal("uncertain move replayed or changed facts")
	}
}
