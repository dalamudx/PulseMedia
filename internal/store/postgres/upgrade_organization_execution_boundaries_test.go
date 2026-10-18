package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type heldNativeLinkFS struct {
	ports.TransferFilesystem
	entered, proceed, released chan struct{}
	open                       sync.Once
	calls                      atomic.Int32
	returnError                bool
	after                      func()
}

func (f *heldNativeLinkFS) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	roots, release, e := f.TransferFilesystem.LockRoots(ctx, paths...)
	if e != nil {
		return nil, nil, e
	}
	return roots, func() { release(); close(f.released) }, nil
}
func (f *heldNativeLinkFS) Hardlink(ctx context.Context, src ports.TransferRoot, sr string, dst ports.TransferRoot, tr string, id ports.FileIdentity) error {
	f.calls.Add(1)
	close(f.entered)
	<-f.proceed
	e := f.TransferFilesystem.Hardlink(context.WithoutCancel(ctx), src, sr, dst, tr, id)
	if f.after != nil {
		f.after()
	}
	if f.returnError {
		return ports.ErrFileConflict
	}
	return e
}
func newHeldNativeLinkFS() *heldNativeLinkFS {
	return &heldNativeLinkFS{TransferFilesystem: rootfs.NewFilesystem(), entered: make(chan struct{}), proceed: make(chan struct{}), released: make(chan struct{})}
}
func (f *heldNativeLinkFS) resume() { f.open.Do(func() { close(f.proceed) }) }
func executionCounts(t *testing.T, f *organizationJobFixture) (int, int) {
	t.Helper()
	var permits, results int
	if e := f.p.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_organization_execution_permit),(SELECT count(*) FROM movie_upgrade_organization_execution_result)`).Scan(&permits, &results); e != nil {
		t.Fatal(e)
	}
	return permits, results
}
func TestMovieUpgradeOrganizationExecutionRejectedPG(t *testing.T) {
	for _, mode := range []string{"wrong_claim", "disabled", "source_replaced", "old_replaced", "target_exists", "source_symlink"} {
		t.Run(mode, func(t *testing.T) {
			f, lease, files, r := executionFixture77(t)
			switch mode {
			case "wrong_claim":
				lease.Token++
			case "disabled":
				f.exec(t, `UPDATE subscription SET upgrade_mode='disabled',intent_version=intent_version+1 WHERE id=$1`, f.sub)
			case "source_replaced":
				raw, e := os.ReadFile(f.sourcePath)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(f.sourcePath, f.sourcePath+".original"); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(f.sourcePath, raw, 0600); e != nil {
					t.Fatal(e)
				}
			case "old_replaced":
				var path string
				if e := f.p.QueryRow(context.Background(), `SELECT $1::text||'/'||f.rel_path FROM library_file f JOIN movie_upgrade_authority a ON a.old_file_id=f.id WHERE a.operation_id=$2`, f.rootPath, f.operation).Scan(&path); e != nil {
					t.Fatal(e)
				}
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(path, path+".original"); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(path, raw, 0600); e != nil {
					t.Fatal(e)
				}
			case "target_exists":
				path := executionTarget(t, f)
				if e := os.MkdirAll(path[:len(path)-len(baseName(path))], 0755); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(path, []byte("owned target"), 0600); e != nil {
					t.Fatal(e)
				}
			case "source_symlink":
				if e := os.Rename(f.sourcePath, f.sourcePath+".original"); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(f.sourcePath+".original", f.sourcePath); e != nil {
					t.Fatal(e)
				}
			}
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			if out, e := r.ExecuteMovieUpgradeOrganization(context.Background(), f.actor, lease); e == nil || out != nil {
				t.Fatal("invalid execution accepted", out, e)
			}
			permits, results := executionCounts(t, f)
			if permits != 0 || results != 0 || files.calls.Load() != 0 {
				t.Fatal(permits, results, files.calls.Load())
			}
			if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) {
				t.Fatal("protected graph changed")
			}
		})
	}
}
func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
func TestMovieUpgradeOrganizationExecutionReturnPG(t *testing.T) {
	for _, mode := range []string{"returned_error", "target_changed"} {
		t.Run(mode, func(t *testing.T) {
			f, lease, _, _ := executionFixture77(t)
			files := newHeldNativeLinkFS()
			files.resume()
			if mode == "returned_error" {
				files.returnError = true
			} else {
				target := executionTarget(t, f)
				files.after = func() {
					if e := os.Remove(target); e != nil {
						t.Error(e)
					}
					if e := os.WriteFile(target, []byte("replacement"), 0600); e != nil {
						t.Error(e)
					}
				}
			}
			r := executionRepository(t, f.p, files)
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			out, e := r.ExecuteMovieUpgradeOrganization(context.Background(), f.actor, lease)
			if e != nil || out == nil || out.Status != "manual_review" || out.ExecutionAllowed {
				t.Fatal(out, e)
			}
			if files.calls.Load() != 1 || before != nativeProtectedGraph(t, f.preparedUpgradeFixture) {
				t.Fatal("changed old files or facts")
			}
			if got, e := r.ExecuteMovieUpgradeOrganization(context.Background(), f.actor, lease); e != nil || got == nil || *got != *out || files.calls.Load() != 1 {
				t.Fatal(got, e)
			}
		})
	}
}
func TestMovieUpgradeOrganizationExecutionLifetimePG(t *testing.T) {
	for _, mode := range []string{"cancel_ignored", "connection_lost", "lease_expired", "account_revoked", "intent_changed"} {
		t.Run(mode, func(t *testing.T) {
			ttl := 2 * time.Minute
			if mode == "lease_expired" {
				ttl = 5 * time.Second
			}
			f, lease, _, _ := executionFixture77(t, ttl)
			files := newHeldNativeLinkFS()
			r := executionRepository(t, f.p, files)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				out *ports.MovieUpgradeOrganizationExecutionResult
				e   error
			}
			returned := make(chan result, 1)
			done := make(chan struct{})
			go func() {
				out, e := r.ExecuteMovieUpgradeOrganization(ctx, f.actor, lease)
				returned <- result{out, e}
				close(done)
			}()
			t.Cleanup(func() {
				files.resume()
				select {
				case <-done:
				case <-time.After(35 * time.Second):
					t.Error("native execution did not drain")
				}
			})
			select {
			case <-files.entered:
			case result := <-returned:
				t.Fatal("rejected before native call", result)
			case <-time.After(20 * time.Second):
				t.Fatal("native call not entered")
			}
			switch mode {
			case "cancel_ignored":
				cancel()
			case "connection_lost":
				var pid int
				if e := f.p.QueryRow(context.Background(), `SELECT backend_pid FROM movie_upgrade_organization_execution_permit WHERE job_id=$1`, lease.ID).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				f.exec(t, `SELECT pg_terminate_backend($1)`, pid)
			case "lease_expired":
				f.exec(t, `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM(lease_until-clock_timestamp())))+0.1) FROM movie_upgrade_organization_execution_permit WHERE job_id=$1`, lease.ID)
				var expired bool
				if e := f.p.QueryRow(context.Background(), `SELECT lease_until<=clock_timestamp() FROM movie_upgrade_organization_execution_permit WHERE job_id=$1`, lease.ID).Scan(&expired); e != nil || !expired {
					t.Fatal(expired, e)
				}
			case "account_revoked":
				f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1::uuid`, f.actor.ID)
			case "intent_changed":
				f.exec(t, `UPDATE subscription SET upgrade_mode='disabled',intent_version=intent_version+1 WHERE id=$1`, f.sub)
			}
			select {
			case <-files.released:
				t.Fatal("physical slot released during call")
			default:
			}
			lock, cancelLock := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_, release, e := rootfs.NewFilesystem().LockRoots(lock, f.rootPath, f.sourceRoot)
			cancelLock()
			if e == nil {
				release()
				t.Fatal("other executor entered physical root")
			}
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			files.resume()
			var got result
			select {
			case got = <-returned:
			case <-time.After(30 * time.Second):
				t.Fatal("native return not drained")
			}
			if mode == "cancel_ignored" {
				if got.e != nil || got.out == nil || got.out.Status != "linked_pending_ingestion" {
					t.Fatal(got)
				}
			} else if got.e == nil || got.out != nil {
				t.Fatal("unsafe late return persisted", got)
			}
			permits, results := executionCounts(t, f)
			want := 0
			if mode == "cancel_ignored" {
				want = 1
			}
			if permits != 1 || results != want || files.calls.Load() != 1 {
				t.Fatal(permits, results, files.calls.Load())
			}
			if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) {
				t.Fatal("protected graph changed")
			}
			if mode == "account_revoked" {
				f.exec(t, `UPDATE app_user SET is_active=true WHERE id=$1::uuid`, f.actor.ID)
			}
			again, err := r.ExecuteMovieUpgradeOrganization(context.Background(), f.actor, lease)
			if mode == "cancel_ignored" {
				if err != nil || again == nil {
					t.Fatal(again, err)
				}
			} else if !errors.Is(err, ports.ErrMovieUpgradeOrganizationReview) {
				t.Fatal("orphan permit not retained", again, err)
			}
			if files.calls.Load() != 1 {
				t.Fatal("native call repeated")
			}
		})
	}
}
func TestMovieUpgradeOrganizationExecutionTailPG(t *testing.T) {
	for _, mode := range []string{"permit_first_row", "result_first_row", "permit_account", "result_account", "result_ledger", "result_requires_terminal"} {
		t.Run(mode, func(t *testing.T) {
			f, lease, files, r := executionFixture77(t)
			switch mode {
			case "permit_first_row":
				f.exec(t, `CREATE FUNCTION execution_fault() RETURNS TRIGGER LANGUAGE plpgsql AS $$BEGIN NEW.permitted_at:=NEW.permitted_at+interval '1 microsecond';RETURN NEW;END $$;CREATE TRIGGER zz_execution_fault BEFORE INSERT ON movie_upgrade_organization_execution_permit FOR EACH ROW EXECUTE FUNCTION execution_fault()`)
			case "result_first_row":
				f.exec(t, `CREATE FUNCTION execution_fault() RETURNS TRIGGER LANGUAGE plpgsql AS $$BEGIN NEW.evidence:=NEW.evidence||'{"reason":"changed"}'::jsonb;RETURN NEW;END $$;CREATE TRIGGER zz_execution_fault BEFORE INSERT ON movie_upgrade_organization_execution_result FOR EACH ROW EXECUTE FUNCTION execution_fault()`)
			case "permit_account", "result_account":
				action := "upgrade.organization-physical-permit:"
				if mode == "result_account" {
					action = "upgrade.organization-physical-result:"
				}
				f.exec(t, fmt.Sprintf(`CREATE FUNCTION execution_fault()RETURNS TRIGGER LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key LIKE '%s%%' THEN UPDATE app_user SET is_active=false WHERE id='%s';END IF;RETURN NEW;END $$;CREATE TRIGGER zz_execution_fault BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION execution_fault()`, action, f.actor.ID))
			case "result_ledger":
				f.exec(t, `CREATE FUNCTION execution_fault()RETURNS TRIGGER LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key LIKE 'upgrade.organization-physical-result:%' THEN UPDATE idempotency_record SET response_body='{}'::bytea WHERE idempotency_key LIKE 'upgrade.organization-physical-permit:%';END IF;RETURN NEW;END $$;CREATE TRIGGER zz_execution_fault BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION execution_fault()`)
			case "result_requires_terminal":
				f.exec(t, `CREATE FUNCTION execution_fault() RETURNS TRIGGER LANGUAGE plpgsql AS $$BEGIN SET CONSTRAINTS upgrade_organization_execution_result_binding IMMEDIATE;RETURN NEW;END $$;CREATE TRIGGER zz_execution_fault AFTER INSERT ON movie_upgrade_organization_execution_result FOR EACH ROW EXECUTE FUNCTION execution_fault()`)
			}
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			out, e := r.ExecuteMovieUpgradeOrganization(context.Background(), f.actor, lease)
			if e == nil || out != nil {
				t.Fatal("tail mutation committed", out, e)
			}
			permits, results := executionCounts(t, f)
			want := 1
			if mode == "permit_first_row" || mode == "permit_account" {
				want = 0
			}
			if permits != want || results != 0 {
				t.Fatal(permits, results)
			}
			if files.calls.Load() != int32(want) {
				t.Fatal("unexpected physical call", files.calls.Load())
			}
			if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) {
				t.Fatal("protected graph changed")
			}
			var active bool
			if e = f.p.QueryRow(context.Background(), `SELECT is_active FROM app_user WHERE id=$1::uuid`, f.actor.ID).Scan(&active); e != nil || !active {
				t.Fatal("tail account change survived", active, e)
			}
		})
	}
}
