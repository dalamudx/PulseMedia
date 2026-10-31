//go:build linux

package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestMP19ExternalCanonicalEpisodeRuntimeCookiePG(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		name := "standard"
		if absolute {
			name = "absolute_metadata"
		}
		t.Run(name, func(t *testing.T) { testMP19ExternalCanonicalEpisodeRuntime(t, absolute) })
	}
}

func testMP19ExternalCanonicalEpisodeRuntime(t *testing.T, absolute bool) {
	m8UntrackedOrganizationRequired(t)
	f := m8UntrackedOrganizationNew(t, "hardlink", "tv")
	// Trusted metadata is prepared independently; the original suggestion grants no import.
	mid, unit := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO media(id,title,original_title,year,media_type) VALUES($1,'Series','Series',2026,'tv')`, mid)
	f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.tv','551')`, uuid.New(), mid)
	var absoluteNumber *int
	if absolute {
		n := 103
		absoluteNumber = &n
	}
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number,absolute_number) VALUES($1,$2,'S01E07',1,7,$3)`, unit, mid, absoluteNumber)
	f.exec(t, `INSERT INTO media_unit_identity(unit_id,namespace,external_id,tmdb_series_id) VALUES($1,'tmdb.episode',55107,551)`, unit)
	f.start(t)
	scopeRaw := f.expect(t, http.MethodGet, "/api/v1/library/untracked-scopes", "", "", http.StatusOK)
	var scopePage ports.UntrackedReviewScopePage
	if err := json.Unmarshal(scopeRaw, &scopePage); err != nil || len(scopePage.Items) != 1 || scopePage.Items[0].ID != f.scope.String() || scopePage.Items[0].LocalRoot != f.source || scopePage.Items[0].TargetRootPath != f.target || !scopePage.Items[0].Enabled {
		t.Fatal("production owner scope picker unavailable", err)
	}
	relative := "[Anime] 任意名称 - 103.mkv"
	content := []byte("explicit canonical episode source bytes")
	if err := os.WriteFile(filepath.Join(f.source, relative), content, 0600); err != nil {
		t.Fatal(err)
	}
	var file uuid.UUID
	var revision int64
	f.wait(t, "native observed arbitrary filename", func() bool {
		return f.pool.QueryRow(t.Context(), `SELECT id,revision FROM untracked_discovered_file WHERE scope_id=$1 AND relative_path=$2`, f.scope, relative).Scan(&file, &revision) == nil
	})
	base := f.path() + "/discoveries/" + file.String()
	raw := f.expect(t, http.MethodGet, base+"/episode-options?tmdb_id=551&season=1", "", "", http.StatusOK)
	var options struct {
		Items []ports.UntrackedReviewEpisodeOption `json:"items"`
	}
	if err := json.Unmarshal(raw, &options); err != nil || len(options.Items) != 1 {
		t.Fatal("trusted episode choices", err, string(raw))
	}
	o := options.Items[0]
	input := ports.UntrackedReviewInput{ScopeVersion: 1, FileRevision: revision, MediaType: "tv", TMDBID: 551, CanonicalUnitID: o.CanonicalUnitID, MediaMetadataVersion: o.MediaMetadataVersion, UnitMetadataVersion: o.UnitMetadataVersion, Season: o.Season, Episode: o.Episode, TMDBEpisodeID: o.TMDBEpisodeID}
	raw = f.expect(t, http.MethodPost, base+"/reviews", m8UntrackedOrganizationJSON(t, input), "mp19-review", http.StatusAccepted)
	var request ports.UntrackedReviewRequestReceipt
	if err := json.Unmarshal(raw, &request); err != nil || request.Request.Input != input {
		t.Fatal("canonical input lost in production API", err)
	}
	var view ports.UntrackedReview
	f.wait(t, "explicit canonical preview", func() bool {
		code, body, err := f.call(t, http.MethodGet, base+"/reviews/"+request.Request.ID.String(), "", "", f.cookie, "")
		return err == nil && code == http.StatusOK && json.Unmarshal(body, &view) == nil && view.State == "ready"
	})
	if view.Stale || view.Evidence == nil || view.Evidence.Candidate == nil || len(view.Evidence.Candidate.Units) != 1 || view.Evidence.Candidate.Units[0].TMDBEpisodeID != 55107 {
		t.Fatal("explicit canonical coverage changed")
	}
	target := "Series (2026)/Season 01/Series.S01E07.mkv"
	confirmation := ports.UntrackedConfirmationInput{ReviewID: request.Request.ID, MaterialHash: view.MaterialHash, ScopeVersion: 1, FileRevision: revision, CandidateKey: view.Evidence.Candidate.Key, Action: "hardlink", TargetRelativePath: target}
	raw = f.expect(t, http.MethodPost, base+"/confirmations", m8UntrackedOrganizationJSON(t, confirmation), "mp19-confirm", http.StatusOK)
	var confirmed ports.UntrackedConfirmationReceipt
	if err := json.Unmarshal(raw, &confirmed); err != nil {
		t.Fatal(err)
	}
	raw = f.expect(t, http.MethodPost, base+"/organizations", m8UntrackedOrganizationJSON(t, ports.UntrackedOrganizationInput{ConfirmationID: confirmed.Material.ID}), "mp19-organize", http.StatusAccepted)
	var queued ports.UntrackedOrganizationReceipt
	if err := json.Unmarshal(raw, &queued); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "canonical episode physically imported", func() bool {
		return f.predicate(t, `SELECT EXISTS(SELECT 1 FROM untracked_organization_completion WHERE operation_id=$1)`, queued.Operation.ID)
	})
	f.stop(t)
	actual, err := os.ReadFile(filepath.Join(f.target, target))
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatal("actual canonical import bytes", err)
	}
	source, err := os.ReadFile(filepath.Join(f.source, relative))
	if err != nil || !bytes.Equal(source, content) {
		t.Fatal("source changed", err)
	}
	sourceIdentity := m8UntrackedOrganizationIdentity(t, f.source, relative)
	targetIdentity := m8UntrackedOrganizationIdentity(t, f.target, target)
	if sourceIdentity.Device != targetIdentity.Device || sourceIdentity.Inode != targetIdentity.Inode {
		t.Fatal("not actual hardlink")
	}
	f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported' AND unit_id=$1`, 1, unit)
	f.count(t, `SELECT count(*) FROM untracked_organization_unit WHERE operation_id=$1 AND unit_id=$2 AND tmdb_episode_id=55107`, 1, queued.Operation.ID, unit)
	f.count(t, `SELECT count(*) FROM download_task`, 0)
	f.count(t, `SELECT count(*) FROM wanted_item`, 0)
	if f.calls.Load() != 0 {
		t.Fatal("external manual mapping called downloader")
	}
	t.Log("MP19_CANONICAL_IMPORT_PASS PG=18.6 App=REAL Cookie=REAL TMDB=NATIVE_OWNED_HTTP filename=ARBITRARY episode=S01E07 imported=1 hardlink=REAL downloader_calls=0")
}
