//go:build linux

package app

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestMP20CompanionsActualRouterCookiePG(t *testing.T) {
	f, op, _, _ := mp20Parent(t, "move")
	mp20Write(t, f, "Movie.2026.en.srt", mp20SRT)
	f.start(t)
	defer f.stop(t)
	base := "/api/v1/library/untracked-organizations/" + op.String() + "/companions"
	raw := f.expect(t, http.MethodGet, base, "", "", http.StatusOK)
	var view ports.CompanionView
	if json.Unmarshal(raw, &view) != nil || view.VideoState != "completed" || len(view.Items) != 0 {
		t.Fatal("actual App read did not report independent video state")
	}
	raw = f.expect(t, http.MethodPost, base+"/discover", "", "", http.StatusOK)
	var candidates struct {
		Items []ports.CompanionCandidate `json:"items"`
	}
	if json.Unmarshal(raw, &candidates) != nil || len(candidates.Items) != 1 || candidates.Items[0].Identity != (ports.FileIdentity{}) {
		t.Fatal("actual discovery leaked identity or lost frozen moved-video stem")
	}
	f.expect(t, http.MethodPost, base+"/discover", " ", "", http.StatusBadRequest)
	body := m8UntrackedOrganizationJSON(t, ports.UntrackedCompanionSelection{CandidateKeys: []string{candidates.Items[0].Key}, Action: "copy"})
	raw = f.expect(t, http.MethodPost, base, body, "actual-companion", http.StatusOK)
	if json.Unmarshal(raw, &view) != nil || len(view.Items) != 1 || view.Items[0].State != "pending" {
		t.Fatal("actual selection not independently pending")
	}
	// Cross-site Cookie writes stop before dispatch; response remains private on early denial.
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.endpoint+base+"/"+view.Items[0].ID.String()+"/execute", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(f.cookie)
	request.Header.Set("Origin", "https://cross-site.invalid")
	response, err := f.httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("actual CSRF denial not private %d %v", response.StatusCode, response.Header)
	}
	f.count(t, `SELECT count(*) FROM untracked_companion WHERE content IS NOT NULL`, 0)
	f.expect(t, http.MethodPost, base+"/"+view.Items[0].ID.String()+"/execute", "", "", http.StatusOK)
	raw = f.expect(t, http.MethodGet, base, "", "", http.StatusOK)
	if json.Unmarshal(raw, &view) != nil || view.Items[0].State != "completed" || view.VideoState != "completed" {
		t.Fatal("actual per-attachment completion not exposed")
	}
	if content, err := os.ReadFile(filepath.Join(f.target, view.Items[0].Candidate.TargetRelativePath)); err != nil || string(content) != mp20SRT {
		t.Fatal("actual App did not publish playable language filename", err)
	}
	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, f.endpoint+base, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = f.httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("anonymous companion result not denied privately")
	}
}
