package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Exercise the actual App-installed service and generated route before the
// existing native test proceeds to acquisition/HTTP I/O. The query cannot queue it.
func probeTVSelectionApp(t *testing.T, client *http.Client, base string, f *completeTVFixture) {
	t.Helper()
	selectionProject(t, f)
	before := f.work(t)
	r := ports.TVSelectionRequest{SubscriptionID: f.f.sub.Id, Season: 1, PackPolicy: quality.PackPolicy{Mode: quality.PackModeRequirePack, MinMissingRatio: 1}, Packs: []ports.TVSelectionCandidateRef{{CandidateID: f.candidate, ReviewID: f.command.ReviewID}}}
	request, err := http.NewRequest("POST", base+"/api/v1/subscriptions/"+f.f.sub.Id.String()+"/tv-selection-preview", strings.NewReader(selectionPublicBody(r)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", base)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: f.f.admin})
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status           string `json:"status"`
		ExecutionAllowed bool   `json:"execution_allowed"`
		Decision         struct {
			Kind string `json:"kind"`
		} `json:"decision"`
	}
	if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "private, no-store" || json.Unmarshal(raw, &result) != nil || result.Status != "preview_not_executable" || result.ExecutionAllowed || result.Decision.Kind != "pack" {
		t.Fatalf("App preview: %d %s", response.StatusCode, raw)
	}
	if before != f.work(t) {
		t.Fatal("App preview created persistent work")
	}
}
