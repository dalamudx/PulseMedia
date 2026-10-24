package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func setCompleteTVNaming(t *testing.T, f *completeOrganizationFixture, template string) int64 {
	t.Helper()
	raw, e := json.Marshal(naming.TemplateRules{EpisodeTemplate: template})
	if e != nil {
		t.Fatal(e)
	}
	row, e := f.n.f.a.Queries().SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: raw})
	if e != nil {
		t.Fatal(e)
	}
	return row.Version
}
func readCompleteTVNaming(t *testing.T, f *completeOrganizationFixture) ports.CompleteTVOrganizationPreview {
	t.Helper()
	w := previewCall(organizationPreviewRouter(f), f, f.n.f.admin, "")
	var page ports.CompleteTVOrganizationPreviewPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 {
		t.Fatalf("naming preview status=%d", w.Code)
	}
	return page.Items[0]
}
func completeTVNamingBody(f *completeOrganizationFixture, version int64) string {
	raw, _ := json.Marshal(ports.CompleteTVOrganizationReferences{TaskID: f.command.TaskID, OperationID: f.command.OperationID, TrackingJobID: f.command.TrackingJobID, NamingVersion: version})
	// Task identity is supplied by the URL, not the public JSON body.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "task_id")
	raw, _ = json.Marshal(fields)
	return string(raw)
}

func TestM9CompleteTVNamingPreviewVersionAndFrozenRecoveryPG(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	first := setCompleteTVNaming(t, f, "Original/{title}/Season {season}/{episode_key}.{extension}")
	p := readCompleteTVNaming(t, f)
	if p.NamingVersion != first || !p.CanConfirm || len(p.Videos) != 2 {
		t.Fatal("new preview did not freeze naming version")
	}
	for _, v := range p.Videos {
		if !strings.HasPrefix(v.TargetRelative, "Original/") || !strings.HasSuffix(v.TargetRelative, v.UnitKey+".mkv") {
			t.Fatal("preview ignored configured sparse unit", v.UnitKey)
		}
	}
	second := setCompleteTVNaming(t, f, "Changed/{title}/Season {season}/{episode_key}.{extension}")
	before := organizationPublicCounts(t, f)
	h := organizationPublicRouter(f, true)
	denied := organizationPublicCall(h, f, completeTVNamingBody(f, first), "m9-naming-stale-preview", f.n.f.admin, nil)
	if denied.Code != http.StatusConflict || organizationPublicCounts(t, f) != before {
		t.Fatal("old preview silently used a newer template", denied.Code)
	}
	p = readCompleteTVNaming(t, f)
	if p.NamingVersion != second {
		t.Fatal("new preview did not reflect committed configuration")
	}
	body := completeTVNamingBody(f, second)
	accepted := organizationPublicCall(h, f, body, "m9-naming-original", f.n.f.admin, nil)
	receipt := organizationPublicReceipt(t, accepted)
	steps, e := f.n.f.a.Queries().ListStepsForPlan(t.Context(), receipt.PlanID)
	if e != nil || len(steps) != 2 {
		t.Fatal("confirmed plan lost exact videos", e)
	}
	for _, step := range steps {
		var frozen struct {
			Version int64 `json:"naming_version"`
		}
		if json.Unmarshal(step.TargetEvidence, &frozen) != nil || frozen.Version != second || !strings.HasPrefix(step.TargetRel.String, "Changed/") {
			t.Fatal("plan diverged from preview")
		}
		if step.TargetPath != filepath.Join(f.target, step.TargetRel.String) {
			t.Fatal("relative target escaped selected root")
		}
	}
	// Current configuration no longer decodes; original receipts and frozen plans
	// must remain recoverable without interpreting or repairing it.
	if _, e = f.n.f.a.Queries().SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	before = organizationPublicCounts(t, f)
	replay := organizationPublicCall(h, f, body, "m9-naming-original", f.n.f.admin, nil)
	if replay.Code != http.StatusAccepted || !bytes.Equal(replay.Body.Bytes(), accepted.Body.Bytes()) || organizationPublicCounts(t, f) != before {
		t.Fatal("original receipt reinterpreted current naming rules", replay.Code)
	}
	frozenPreview := readCompleteTVNaming(t, f)
	if frozenPreview.CanConfirm || frozenPreview.NamingVersion != second || len(frozenPreview.Videos) != 2 {
		t.Fatal("existing plan preview read corrupted configuration")
	}
	for _, v := range frozenPreview.Videos {
		if !strings.HasPrefix(v.TargetRelative, "Changed/") {
			t.Fatal("existing plan preview changed target")
		}
	}
	if e = f.run(t, &receipt); e != nil {
		t.Fatal("frozen template plan did not execute", e)
	}
	f.assertImported(t, &receipt)
	after, e := f.n.f.a.Queries().ListStepsForPlan(t.Context(), receipt.PlanID)
	if e != nil || len(after) != 2 {
		t.Fatal(e)
	}
	for i, step := range after {
		if step.TargetPath != steps[i].TargetPath || !bytes.Equal(step.TargetEvidence, steps[i].TargetEvidence) {
			t.Fatal("execution mutated original naming evidence")
		}
	}
}

func TestM9CompleteTVNamingInvalidTargetKeepsDemandPG(t *testing.T) {
	for _, template := range []string{"Custom/{title}/{episode_key}.mp4", "Custom/{title}/{imdb_id}/{episode_key}.{extension}"} {
		t.Run(template, func(t *testing.T) {
			f := newCompleteOrganizationFixture(t)
			version := setCompleteTVNaming(t, f, template)
			before := organizationPublicCounts(t, f)
			w := organizationPublicCall(organizationPublicRouter(f, true), f, completeTVNamingBody(f, version), "m9-naming-invalid", f.n.f.admin, nil)
			if w.Code != http.StatusConflict || organizationPublicCounts(t, f) != before {
				t.Fatal("invalid template created plan, receipt or consumed demand", w.Code)
			}
			if f.n.adds.Load() != 0 {
				t.Fatal("organization sent downloader Add")
			}
		})
	}
}
