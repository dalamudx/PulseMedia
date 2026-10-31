package transfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transfercase "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
)

func setAutomaticTVNaming(t *testing.T, f *automaticTVOrganizationFixture, rules naming.TemplateRules) int64 {
	t.Helper()
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.q.SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: raw})
	if err != nil {
		t.Fatal(err)
	}
	return row.Version
}

func TestAutomaticTVNamingFrozenNativePlanPG(t *testing.T) {
	f := newAutomaticTVOrganizationFixture(t)
	version := setAutomaticTVNaming(t, f, naming.TemplateRules{EpisodeTemplate: "Custom/{title} [{tmdb_id}]/Season {season}/{episode_key}.{extension}"})
	if err := f.svc.OrganizeDownload(t.Context(), f.lease); err != nil {
		t.Fatal("custom automatic TV organization", err)
	}
	f.assertImported(t)
	plan, err := f.q.GetTransferPlanByOpID(t.Context(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := f.q.ListStepsForPlan(t.Context(), plan.ID)
	if err != nil || len(steps) != 1 {
		t.Fatal("original step missing", err)
	}
	expected := filepath.Join(f.rootPath, "Custom/Trusted Native Series [55101]/Season 02/S02E03.mkv")
	if steps[0].TargetPath != expected {
		t.Fatal("automatic TV ignored configured path", steps[0].TargetPath)
	}
	var evidence transferTargetEvidence
	if json.Unmarshal(steps[0].TargetEvidence, &evidence) != nil || evidence.NamingVersion != version {
		t.Fatal("version not frozen with step")
	}
	dedicated, err := f.q.AutomaticTVOrganizationPlan(t.Context(), f.operation)
	if err != nil || !bytes.Equal(dedicated.TargetEvidence, steps[0].TargetEvidence) {
		t.Fatal("dedicated evidence diverged", err)
	}
	setAutomaticTVNaming(t, f, naming.TemplateRules{EpisodeTemplate: "Changed/{title}/{episode_key}.{extension}"})
	handled, err := f.svc.confirmAutomaticTVOrganization(t.Context(), f.lease, plan)
	if err != nil || !handled {
		t.Fatal("later template withdrew original completion", err)
	}
	f.assertImported(t)
	if _, err = os.Stat(filepath.Join(f.rootPath, "Changed")); !os.IsNotExist(err) {
		t.Fatal("completed task created a new destination", err)
	}
}

func TestAutomaticTVNamingRejectsInvalidTargetBeforePlanPG(t *testing.T) {
	for _, template := range []string{"Custom/{title}/{episode_key}.mp4", "Custom/{title}/{imdb_id}/{episode_key}.{extension}"} {
		t.Run(template, func(t *testing.T) {
			f := newAutomaticTVOrganizationFixture(t)
			setAutomaticTVNaming(t, f, naming.TemplateRules{EpisodeTemplate: template})
			if err := f.svc.OrganizeDownload(t.Context(), f.lease); !errors.Is(err, ErrReviewRequired) {
				t.Fatal("invalid target did not require review", err)
			}
			f.assertUnpublished(t)
			if automaticTVOrganizationCount(t, f.pool, `SELECT count(*) FROM transfer_plan`) != 0 || automaticTVOrganizationCount(t, f.pool, `SELECT count(*) FROM automatic_tv_organization_plan`) != 0 {
				t.Fatal("invalid target left authorized physical work")
			}
		})
	}
}

func TestAutomaticTVNamingPublishedRollbackKeepsOriginalTargetPG(t *testing.T) {
	f := newAutomaticTVOrganizationFixture(t)
	version := setAutomaticTVNaming(t, f, naming.TemplateRules{EpisodeTemplate: "Original/{title}/Season {season}/{episode_key}.{extension}"})
	automaticTVOrganizationExec(t, f.pool, `CREATE FUNCTION naming_tv_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'naming TV tail'; END $$`)
	automaticTVOrganizationExec(t, f.pool, `CREATE CONSTRAINT TRIGGER naming_tv_fault AFTER INSERT ON automatic_tv_organization_completion DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION naming_tv_fault()`)
	if err := f.svc.OrganizeDownload(t.Context(), f.lease); err == nil {
		t.Fatal("injected completion failure published facts")
	}
	f.assertUnpublished(t)
	plan, err := f.q.GetTransferPlanByOpID(t.Context(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := f.q.ListStepsForPlan(t.Context(), plan.ID)
	if err != nil || len(steps) != 1 {
		t.Fatal("original physical step lost", err)
	}
	before, err := os.Stat(steps[0].TargetPath)
	if err != nil {
		t.Fatal("published target missing", err)
	}
	source, err := os.Stat(f.sourcePath)
	if err != nil || !os.SameFile(source, before) {
		t.Fatal("original publication is not the source inode", err)
	}
	var evidence transferTargetEvidence
	if json.Unmarshal(steps[0].TargetEvidence, &evidence) != nil || evidence.NamingVersion != version {
		t.Fatal("original version missing")
	}
	// Even corrupt current rules cannot authorize a replacement or block recovery.
	if _, err = f.q.SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	automaticTVOrganizationExec(t, f.pool, `DROP TRIGGER naming_tv_fault ON automatic_tv_organization_completion`)
	automaticTVOrganizationExec(t, f.pool, `UPDATE transfer_plan SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, plan.ID)
	automaticTVOrganizationExec(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.lease.ID)
	automaticTVOrganizationExec(t, f.pool, `UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL WHERE id=$1`, f.lease.ID)
	f.lease = automaticTVOrganizationClaim(t, f.q, "download_organization")
	f.svc = NewService(f.pool, idgen.New(), outbox.New(f.pool, idgen.New(), clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
	if err = f.svc.OrganizeDownload(t.Context(), f.lease); err != nil {
		t.Fatal("recovery consulted changed naming rules", err)
	}
	f.assertImported(t)
	current, err := f.q.GetTransferPlanByOpID(t.Context(), f.operation)
	if err != nil || current.ID != plan.ID {
		t.Fatal("recovery replaced the plan", err)
	}
	after, err := f.q.ListStepsForPlan(t.Context(), plan.ID)
	if err != nil || len(after) != 1 || after[0].TargetPath != steps[0].TargetPath || !bytes.Equal(after[0].TargetEvidence, steps[0].TargetEvidence) {
		t.Fatal("recovery changed the frozen target", err)
	}
	target, err := os.Stat(after[0].TargetPath)
	if err != nil || !os.SameFile(before, target) {
		t.Fatal("recovery republished the original target", err)
	}
}
