package transfer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func TestLibraryReuseNamingPreservesExplicitTargetPG(t *testing.T) {
	for _, action := range []string{"hardlink", "copy"} {
		for _, stage := range []string{"normal", "published-tail"} {
			t.Run(action+"/"+stage, func(t *testing.T) {
				f, r, a, target, file := reusePreviewFixture(t)
				rules, _ := json.Marshal(naming.TemplateRules{MovieTemplate: "Different/{title}.{extension}"})
				if _, err := f.q.SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: rules}); err != nil {
					t.Fatal(err)
				}
				c := reuseCommand(t, r, a, target, file, action)
				receipt, err := r.ConfirmLibraryReuse(t.Context(), a, c, "m9-explicit-reuse")
				if err != nil {
					t.Fatal(err)
				}
				original, err := f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
				if err != nil {
					t.Fatal(err)
				}
				beforeDownloads := f.count(t, "download_task")
				if _, err = f.q.SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: []byte(`{}`)}); err != nil {
					t.Fatal(err)
				}
				counter := &mediaFilterReuseFilesystem{VerifiedTransferFilesystem: f.svc.files.(ports.VerifiedTransferFilesystem)}
				f.svc.files = counter
				lease := claimReuse(t, f)
				root, err := f.q.GetLibraryRootByID(t.Context(), target.RootID)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root.Path, c.TargetRel)
				var published os.FileInfo
				if stage == "published-tail" {
					if _, err = f.pool.Exec(t.Context(), `CREATE FUNCTION naming_reuse_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='completed' THEN RAISE EXCEPTION 'naming reuse tail'; END IF; RETURN NEW; END $$; CREATE TRIGGER naming_reuse_tail BEFORE UPDATE ON library_reuse_plan FOR EACH ROW EXECUTE FUNCTION naming_reuse_tail()`); err != nil {
						t.Fatal(err)
					}
					if err = r.ExecuteLibraryReuse(t.Context(), lease); err == nil {
						t.Fatal("injected finalization unexpectedly committed")
					}
					published, err = os.Stat(path)
					if err != nil {
						t.Fatal("original target not published", err)
					}
					if _, err = f.pool.Exec(t.Context(), `DROP TRIGGER naming_reuse_tail ON library_reuse_plan`); err != nil {
						t.Fatal(err)
					}
				}
				if err = r.ExecuteLibraryReuse(t.Context(), lease); err != nil {
					t.Fatal("original reuse target could not recover", err)
				}
				complete, err := f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
				if err != nil || complete.State != "completed" || !bytes.Equal(original.Frozen, complete.Frozen) {
					t.Fatal("frozen reuse material changed", err)
				}
				info, err := os.Stat(path)
				if err != nil || (published != nil && !os.SameFile(published, info)) {
					t.Fatal("recovery replaced original publication", err)
				}
				if (action == "hardlink" && (counter.links != 1 || counter.transfers != 0)) || (action == "copy" && (counter.links != 0 || counter.transfers != 1)) {
					t.Fatal("template update caused a second physical write", counter.links, counter.transfers)
				}
				replay, err := r.ConfirmLibraryReuse(t.Context(), a, c, "m9-explicit-reuse")
				if err != nil || replay == nil || *replay != *receipt {
					t.Fatal("original reuse receipt changed", err)
				}
				if f.count(t, "download_task") != beforeDownloads || !reusePreview(t, r, a, target).AlreadyOwned {
					t.Fatal("reuse fabricated download or lost exact ownership")
				}
				if _, err = os.Stat(filepath.Join(root.Path, "Different")); !os.IsNotExist(err) {
					t.Fatal("new template overrode explicit destination", err)
				}
			})
		}
	}
}
