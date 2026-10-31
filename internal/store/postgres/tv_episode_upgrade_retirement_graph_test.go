package postgres_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestTVEpisodeUpgradeRetirementGraphPG(t *testing.T) {
	for _, change := range []string{"none", "permission", "old-file", "new-file", "new-fact", "root-health", "root-sequence-trigger", "root-timestamp-trigger"} {
		t.Run(change, func(t *testing.T) {
			f := newTVUpgradeFixture(t)
			r := f.prepared(t)
			f.command(t, "send", r.OperationID, r.Version, "send")
			f.command(t, "observe", r.OperationID, r.Version, "observe")
			review := f.command(t, "organization-preview", r.OperationID, r.Version, "organization-preview")
			confirmed := f.command(t, "organization-confirm", r.OperationID, review.Version, "organization-confirm")
			imported := f.command(t, "organize", r.OperationID, confirmed.Version, "organize")
			retirement := f.command(t, "retirement-preview", r.OperationID, imported.Version, "retirement-preview")
			approved := f.command(t, "retirement-confirm", r.OperationID, retirement.Version, "retirement-confirm")
			var oldRel, newRel string
			var beforeSeq int64
			if err := f.pool.QueryRow(t.Context(), "SELECT rel_path FROM library_file WHERE id=$1", f.request.CurrentFileID).Scan(&oldRel); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(t.Context(), "SELECT rel_path FROM library_file WHERE id=$1", imported.NewFileID).Scan(&newRel); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(t.Context(), "SELECT mutation_seq FROM library_root WHERE id=$1", f.tv).Scan(&beforeSeq); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "permission":
				execIntent(t, f.pool, "UPDATE app_user SET role='member' WHERE id=$1", f.actor.ID)
			case "old-file":
				execIntent(t, f.pool, "UPDATE library_file SET mtime_ns=mtime_ns+1 WHERE id=$1", f.request.CurrentFileID)
			case "new-file":
				execIntent(t, f.pool, "UPDATE library_file SET mtime_ns=mtime_ns+1 WHERE id=$1", imported.NewFileID)
			case "new-fact":
				execIntent(t, f.pool, "UPDATE unit_fact SET quality_profile='{}' WHERE source_file_id=$1", imported.NewFileID)
			case "root-health":
				execIntent(t, f.pool, "UPDATE library_root SET health='offline',health_revision=health_revision+1 WHERE id=$1", f.tv)
			case "root-sequence-trigger", "root-timestamp-trigger":
				assignment := "NEW.mutation_seq := NEW.mutation_seq + 10;"
				if change == "root-timestamp-trigger" {
					assignment = "NEW.updated_at := NEW.updated_at + interval '1 second';"
				}
				execIntent(t, f.pool, "CREATE FUNCTION mp22_retirement_tamper() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN "+assignment+" RETURN NEW; END $$")
				execIntent(t, f.pool, "CREATE TRIGGER mp22_retirement_tamper BEFORE UPDATE ON library_root FOR EACH ROW EXECUTE FUNCTION mp22_retirement_tamper()")
			}
			command := ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "retire", OperationID: r.OperationID, ExpectedVersion: approved.Version, Confirmed: true}
			result, err := f.service.Command(t.Context(), f.actor, command, "retire")
			oldPath := filepath.Join(f.rootPath, oldRel)
			recyclePath := filepath.Join(f.rootPath, ".pulsemedia_recycle", "tv-upgrade", r.OperationID.String(), filepath.Base(oldRel))
			physical := change == "none" || change == "root-sequence-trigger" || change == "root-timestamp-trigger"
			if change == "none" {
				if err != nil || result == nil || result.Status != "retired" {
					_, oldErr := os.Stat(oldPath)
					_, recycleErr := os.Stat(recyclePath)
					t.Fatalf("retirement commit failed: result=%+v err=%v root_seq=%d old_exists=%t recycle_exists=%t", result, err, beforeSeq, oldErr == nil, recycleErr == nil)
				}
				var afterSeq int64
				if err := f.pool.QueryRow(t.Context(), "SELECT mutation_seq FROM library_root WHERE id=$1", f.tv).Scan(&afterSeq); err != nil || afterSeq != beforeSeq+1 {
					t.Fatal("retirement root delta", afterSeq, beforeSeq, err)
				}
			} else {
				if err == nil {
					t.Fatal("changed retirement material committed", result)
				}
				if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_phase WHERE action='retire'"); n != 0 {
					t.Fatal("failed retirement left success", n)
				}
				if n := intentCount(t, f.pool, "SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL"); n != 1 {
					t.Fatal("failed retirement lost protection", n)
				}
				if physical {
					if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_permit WHERE action='retire'"); n != 1 {
						t.Fatal("physical retirement lost permanent permit", n)
					}
					if _, err := f.service.Command(t.Context(), f.actor, command, "retire-again"); err == nil {
						t.Fatal("unknown physical retirement replayed")
					}
				}
			}
			preservedPath := oldPath
			if physical {
				preservedPath = recyclePath
				if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
					t.Fatal("old path still present after physical retirement", err)
				}
			}
			oldBytes, err := os.ReadFile(preservedPath)
			if err != nil || !bytes.Equal(oldBytes, bytes.Repeat([]byte("o"), 100)) {
				t.Fatal("old bytes lost", err)
			}
			if info, err := os.Stat(filepath.Join(f.rootPath, newRel)); err != nil || info.Size() != 1234 {
				t.Fatal("new file lost", err)
			}
		})
	}
}
