package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestMovieUpgradeOrganizationPhysicalFilesPG(t *testing.T) {
	for _, kind := range []string{"source-size", "source-symlink", "source-parent-symlink", "old-replaced", "marker-symlink", "target-present"} {
		t.Run(kind, func(t *testing.T) {
			f := physicalOrganizationFixture75(t)
			ctx := context.Background()
			before, old := nativeProtectedGraph(t, f.preparedUpgradeFixture), f.oldState(t)
			switch kind {
			case "source-size":
				if e := os.WriteFile(f.sourcePath, []byte("wrong"), 0600); e != nil {
					t.Fatal(e)
				}
			case "source-symlink":
				saved := f.sourcePath + ".original"
				if e := os.Rename(f.sourcePath, saved); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(saved, f.sourcePath); e != nil {
					t.Fatal(e)
				}
			case "source-parent-symlink":
				original := f.sourceRoot + "-original"
				if e := os.Rename(f.sourceRoot, original); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(original, f.sourceRoot); e != nil {
					t.Fatal(e)
				}
			case "old-replaced":
				path := filepath.Join(f.rootPath, "original.mkv")
				stat, e := os.Stat(path)
				if e != nil {
					t.Fatal(e)
				}
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(path, path+".prior"); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(path, raw, 0600); e != nil {
					t.Fatal(e)
				}
				if e = os.Chtimes(path, stat.ModTime(), stat.ModTime()); e != nil {
					t.Fatal(e)
				}
			case "marker-symlink":
				path := filepath.Join(f.rootPath, ".pulsemedia_root")
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(filepath.Join(f.rootPath, "original.mkv"), path); e != nil {
					t.Fatal(e)
				}
			case "target-present":
				path := filepath.Join(f.rootPath, naming.FormatMovieDir("Fight Club", 1999), naming.FormatMovieFile("Fight Club", 1999, media.Recognize("Fight.Club.1999.2160p.Bluray.x265").Quality, ".mkv"))
				if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(path, []byte("user data"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "bad-files"); e == nil {
				t.Fatal("invalid filesystem accepted")
			}
			f.planCount(t, 0)
			if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) || old != f.oldState(t) {
				t.Fatal("original state changed")
			}
		})
	}
}
func TestMovieUpgradeOrganizationPhysicalTamperingPG(t *testing.T) {
	cases := []struct{ name, table, timing, body string }{
		{"plan-first-row", "transfer_plan", "BEFORE", "NEW.updated_at:=NEW.created_at+interval '1 microsecond';"},
		{"step-first-row", "transfer_step", "BEFORE", "NEW.source_mtime:=NEW.source_mtime+1;"},
		{"preparation-first-row", "movie_upgrade_organization_preparation", "BEFORE", "NEW.prepared_at:=NEW.prepared_at-interval '1 microsecond';"},
		{"actor-tail", "outbox_event", "AFTER", "IF NEW.event_type='upgrade.organization-prepare' THEN UPDATE app_user SET is_active=false;END IF;"},
		{"old-fact-tail", "outbox_event", "AFTER", "IF NEW.event_type='upgrade.organization-prepare' THEN UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE revoked_at IS NULL;END IF;"},
		{"extra-step-tail", "outbox_event", "AFTER", "IF NEW.event_type='upgrade.organization-prepare' THEN INSERT INTO transfer_step SELECT (jsonb_populate_record(NULL::transfer_step,to_jsonb(s)||jsonb_build_object('id',gen_random_uuid(),'step_order',2))).* FROM transfer_step s LIMIT 1;END IF;"},
		{"receipt-tail", "idempotency_record", "BEFORE", "IF NEW.idempotency_key LIKE 'upgrade.organization-prepare:%' THEN NEW.resource_id:=gen_random_uuid()::text;END IF;"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := physicalOrganizationFixture75(t)
			ctx := context.Background()
			before, old := nativeProtectedGraph(t, f.preparedUpgradeFixture), f.oldState(t)
			f.exec(t, `CREATE FUNCTION pulse_test_physical_tamper() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN `+c.body+`RETURN NEW;END$$;CREATE TRIGGER pulse_test_physical_tamper `+c.timing+` INSERT ON `+c.table+` FOR EACH ROW EXECUTE FUNCTION pulse_test_physical_tamper()`)
			if _, e := f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "tampered-physical"); e == nil {
				t.Fatal("tampering accepted")
			}
			f.planCount(t, 0)
			if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) || old != f.oldState(t) {
				t.Fatal("rollback left original changes")
			}
		})
	}
}
func TestMovieUpgradeOrganizationPhysicalClaimGuardPG(t *testing.T) {
	f := physicalOrganizationFixture75(t)
	ctx := context.Background()
	out, e := f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "guarded")
	if e != nil {
		t.Fatal(e)
	}
	for _, query := range []string{`UPDATE transfer_plan SET state='approved',requires_approval=false WHERE id=$1`, `UPDATE transfer_plan SET updated_at=clock_timestamp() WHERE id=$1`, `UPDATE transfer_step SET state='completed' WHERE plan_id=$1`, `DELETE FROM transfer_step WHERE plan_id=$1`, `DELETE FROM transfer_plan WHERE id=$1`} {
		if _, e = f.p.Exec(ctx, query, out.PlanID); e == nil {
			t.Fatal("prepared execution graph mutated", query)
		}
	}
	if _, e = f.p.Exec(ctx, `TRUNCATE movie_upgrade_organization_preparation CASCADE`); e == nil {
		t.Fatal("prepared history truncated")
	}
	// Permission checks run before reading historical receipts; an API credential cannot recover them.
	api := *f.actor
	api.Scopes = []string{"read"}
	if _, e = f.preparation.ReadMovieUpgradeOrganizationPreparationReceipt(ctx, &api, f.operation, "guarded"); e == nil {
		t.Fatal("API actor recovered command")
	}
	other := f.command
	other.OperationID = uuid.New()
	if _, e = f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, other, "guarded"); !errors.Is(e, ports.ErrNotFound) {
		t.Fatal("wrong operation", e)
	}
}
