package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/google/uuid"
)

func TestM9AdvancedFamilyRulesNativeHTTPPG(t *testing.T) { m9MediaRequestNativeFlow(t, true) }

// An explicit administrator binding must remain usable even when a deployment
// rule points elsewhere. Automatic site/media selection has its own native test.
func m9AdvancedFamilyRouting(enabled bool) func(*config.Config) {
	unavailable := uuid.New()
	return func(cfg *config.Config) {
		if enabled {
			cfg.DownloadRouting.Rules = []config.DownloadRoutingRule{{MediaType: "tv", DownloaderID: unavailable}}
		}
	}
}

func m9AdvancedFamilyConfigure(t *testing.T, h *m5AutomaticTVAcceptance, admin string) {
	t.Helper()
	policyID, ok := h.body["quality_policy_id"].(string)
	if !ok || policyID == "" {
		t.Fatal("explicit native policy required")
	}
	read := h.f.request(t, http.MethodGet, "/api/v1/policies/"+policyID+"/revisions/1", "", nil, admin)
	m5IndexerStatus(t, read, http.StatusOK)
	var doc httpapi.PolicyDocument
	if json.Unmarshal(read.body, &doc) != nil || doc.Id != policyID || doc.Revision != 1 {
		t.Fatal("original policy unavailable")
	}
	original := h.f.request(t, http.MethodPost, "/api/v1/policies/preview", "", map[string]any{"policy_id": policyID, "release_title": m5AutomaticTVRelease}, admin)
	m5IndexerStatus(t, original, http.StatusOK)
	var before httpapi.PolicyPreviewResponse
	if json.Unmarshal(original.body, &before) != nil {
		t.Fatal("original quality preview unavailable")
	}
	rules := doc.Rules
	words := []httpapi.QualityWordRule{{Word: "1080p", Required: true, Score: 0}, {Word: "WEB-DL", Score: 77}, {Word: "CAM", Excluded: true, Score: 0}}
	rules.CustomWords = &words
	publish := h.f.request(t, http.MethodPost, "/api/v1/policies/"+policyID+"/revisions", "advanced-family-policy", httpapi.PublishPolicyRequest{ExpectedRevision: 1, Rules: &rules}, admin)
	m5IndexerStatus(t, publish, http.StatusCreated)
	preferred := h.f.request(t, http.MethodPost, "/api/v1/policies/preview", "", map[string]any{"policy_id": policyID, "release_title": m5AutomaticTVRelease}, admin)
	m5IndexerStatus(t, preferred, http.StatusOK)
	var after httpapi.PolicyPreviewResponse
	if json.Unmarshal(preferred.body, &after) != nil || !after.Passed || after.Score != before.Score || after.PreferenceScore == nil || *after.PreferenceScore != 77 || after.MeetsCutoff != before.MeetsCutoff {
		t.Fatal("preferences changed intrinsic quality or failed to admit the native candidate")
	}
	for _, title := range []string{strings.ReplaceAll(m5AutomaticTVRelease, "1080p", "720p"), m5AutomaticTVRelease + ".CAM"} {
		rejected := h.f.request(t, http.MethodPost, "/api/v1/policies/preview", "", map[string]any{"policy_id": policyID, "release_title": title}, admin)
		m5IndexerStatus(t, rejected, http.StatusOK)
		var result httpapi.PolicyPreviewResponse
		if json.Unmarshal(rejected.body, &result) != nil || result.Passed {
			t.Fatal("required or excluded gate admitted a forbidden title")
		}
	}
	naming := h.f.request(t, http.MethodPut, "/api/v1/settings/organization.naming_templates", "advanced-family-naming", map[string]any{"expected_version": 0, "value": map[string]string{"movie_template": "", "episode_template": "M9Joint/{title} ({year})/Season {season}/{episode_key}.{extension}"}}, admin)
	m9MediaRequestStatus(t, naming, http.StatusOK)
}

func m9AdvancedFamilyImported(t *testing.T, h *m5AutomaticTVAcceptance, op uuid.UUID, admin string) {
	t.Helper()
	if h.count(t, `SELECT count(*) FROM subscription WHERE id=$1 AND downloader_id=$2 AND quality_policy_revision=2`, h.subscription, h.client) != 1 {
		t.Fatal("approval selection or policy was redirected")
	}
	var target string
	if err := h.f.app.DBPool().QueryRow(t.Context(), `SELECT f.rel_path FROM library_file f JOIN library_file_unit m ON m.file_id=f.id WHERE m.unit_id=$1 AND f.root_id=$2 AND f.retired_at IS NULL`, h.unit, h.root).Scan(&target); err != nil || !strings.HasPrefix(target, "M9Joint/") || !strings.HasSuffix(target, "/Season 01/S01E03.mkv") {
		t.Fatal("import did not use the saved native naming template", err)
	}
	// Change configuration only after the first plan has published. The remainder
	// of the shared test restores receipts offline, restarts App, and checks the
	// same original inode, task identity and one Add without consuming other demand.
	changed := h.f.request(t, http.MethodPut, "/api/v1/settings/organization.naming_templates", "advanced-family-naming-after-import", map[string]any{"expected_version": 1, "value": map[string]string{"movie_template": "", "episode_template": "NewPlans/{title}/Season {season}/{episode_key}.{extension}"}}, admin)
	m9MediaRequestStatus(t, changed, http.StatusOK)
	if h.count(t, `SELECT count(*) FROM automatic_tv_execution_authority WHERE operation_id=$1 AND client_id=$2`, op, h.client) != 1 {
		t.Fatal("native authority did not retain explicit downloader")
	}
	t.Log("M9_ADVANCED_FAMILY_NATIVE_IMPORT_PASS request=APPROVED policy_revision=2 required=1080p excluded=CAM preference=77 quality=UNCHANGED downloader=EXPLICIT_OVER_DEPLOYMENT_RULE target=M9Joint_S01E03 current_template_version=2 original_bytes=true Add=1 same_inode=true")
}
