//go:build linux

package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type externalBatchLostResponse struct{ next http.RoundTripper }

func (t externalBatchLostResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	v, e := t.next.RoundTrip(r)
	if e != nil {
		return nil, e
	}
	if v.StatusCode >= http.StatusBadRequest {
		return v, nil
	}
	_ = v.Body.Close()
	return nil, errors.New("owned response loss after server commit")
}
func TestExternalBatchRuntimeCookiePG(t *testing.T) {
	m8UntrackedOrganizationRequired(t)
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale_%t", stale), func(t *testing.T) {
			f := m8UntrackedOrganizationNew(t, "hardlink", "tv")
			mid := uuid.New()
			units := []uuid.UUID{uuid.New(), uuid.New()}
			f.exec(t, `INSERT INTO media(id,title,original_title,year,media_type) VALUES($1,'Series','Series',2026,'tv')`, mid)
			f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.tv','551')`, uuid.New(), mid)
			for i, n := range []int{2, 7} {
				f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number,air_date) VALUES($1,$2,$3,1,$4,CASE WHEN $4=2 THEN DATE '2026-01-02' END)`, units[i], mid, fmt.Sprintf("S01E%02d", n), n)
				f.exec(t, `INSERT INTO media_unit_identity(unit_id,namespace,external_id,tmdb_series_id) VALUES($1,'tmdb.episode',$2,551)`, units[i], 55100+n)
			}
			f.start(t)
			input := ports.ExternalBatchInput{}
			for i, n := range []int{2, 7} {
				relative := fmt.Sprintf("batch-%d-任意名称.mkv", i)
				content := []byte(fmt.Sprintf("owned batch bytes %d", i))
				if e := os.WriteFile(filepath.Join(f.source, relative), content, 0600); e != nil {
					t.Fatal(e)
				}
				var file uuid.UUID
				var revision int64
				f.wait(t, "native batch source discovery", func() bool {
					return f.pool.QueryRow(t.Context(), `SELECT id,revision FROM untracked_discovered_file WHERE scope_id=$1 AND relative_path=$2`, f.scope, relative).Scan(&file, &revision) == nil
				})
				base := f.path() + "/discoveries/" + file.String()
				raw := f.expect(t, "GET", base+"/episode-options?tmdb_id=551&season=1", "", "", 200)
				var opts struct {
					Items []ports.UntrackedReviewEpisodeOption `json:"items"`
				}
				if e := json.Unmarshal(raw, &opts); e != nil {
					t.Fatal(e)
				}
				var option ports.UntrackedReviewEpisodeOption
				for _, o := range opts.Items {
					if o.Episode == n {
						option = o
					}
				}
				if option.CanonicalUnitID == "" {
					t.Fatal("missing real canonical unit")
				}
				reviewInput := ports.UntrackedReviewInput{ScopeVersion: 1, FileRevision: revision, MediaType: "tv", TMDBID: 551, CanonicalUnitID: option.CanonicalUnitID, MediaMetadataVersion: option.MediaMetadataVersion, UnitMetadataVersion: option.UnitMetadataVersion, Season: 1, Episode: n, TMDBEpisodeID: option.TMDBEpisodeID}
				raw = f.expect(t, "POST", base+"/reviews", m8UntrackedOrganizationJSON(t, reviewInput), fmt.Sprintf("batch-review-%d", i), 202)
				var req ports.UntrackedReviewRequestReceipt
				if e := json.Unmarshal(raw, &req); e != nil {
					t.Fatal(e)
				}
				var view ports.UntrackedReview
				f.wait(t, "native canonical batch review", func() bool {
					code, body, e := f.call(t, "GET", base+"/reviews/"+req.Request.ID.String(), "", "", f.cookie, "")
					return e == nil && code == 200 && json.Unmarshal(body, &view) == nil && view.State == "ready" && !view.Stale
				})
				input.Rows = append(input.Rows, ports.ExternalBatchSelection{FileID: file, Confirmation: ports.UntrackedConfirmationInput{ReviewID: req.Request.ID, MaterialHash: view.MaterialHash, ScopeVersion: 1, FileRevision: revision, CandidateKey: view.Evidence.Candidate.Key, Action: "hardlink", TargetRelativePath: fmt.Sprintf("Series (2026)/Season 01/Series.S01E%02d.mkv", n)}})
			}
			batchBase := f.path() + "/external-batches"
			key := "batch-lost-preview"
			originalTransport := f.httpClient.Transport
			f.httpClient.Transport = externalBatchLostResponse{originalTransport}
			_, _, e := f.call(t, "POST", batchBase, m8UntrackedOrganizationJSON(t, input), key, f.cookie, "")
			f.httpClient.Transport = originalTransport
			if e == nil {
				t.Fatal("response-loss fixture did not drop response")
			}
			raw := f.expect(t, "GET", f.path()+"/external-batch-commands/"+key, "", "", 200)
			var batch httpapi.ExternalBatchView
			if e = json.Unmarshal(raw, &batch); e != nil || len(batch.Batch.Rows) != 2 {
				t.Fatal("original key recovery", e, string(raw))
			}
			f.count(t, `SELECT count(*) FROM external_import_batch`, 1)
			duplicate := input
			duplicate.Rows = append([]ports.ExternalBatchSelection{}, input.Rows...)
			duplicate.Rows[1] = duplicate.Rows[0]
			f.expect(t, "POST", batchBase, m8UntrackedOrganizationJSON(t, duplicate), "duplicate", 400)
			if stale {
				f.exec(t, `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1`, units[1])
			}
			base := batchBase + "/" + batch.Batch.Id.String()
			completed := 0
			for i := range input.Rows {
				row := fmt.Sprintf("%s/rows/%d", base, i)
				if stale && i == 1 {
					f.expect(t, "POST", row+"/confirm", `{"confirmed":true}`, "", 409)
					continue
				}
				f.expect(t, "POST", row+"/confirm", `{"confirmed":true}`, "", 200)
				f.httpClient.Transport = externalBatchLostResponse{originalTransport}
				organizationStatus, organizationBody, e := f.call(t, "POST", row+"/organize", `{"confirmed":true}`, "", f.cookie, "")
				f.httpClient.Transport = originalTransport
				if e == nil {
					var problem struct {
						Code string `json:"code"`
					}
					_ = json.Unmarshal(organizationBody, &problem)
					t.Fatalf("organization response not dropped: HTTP %d code=%q", organizationStatus, problem.Code)
				}
				var view httpapi.ExternalBatchView
				lastCode, lastState, lastError := 0, "", ""
				defer func() {
					if t.Failed() {
						t.Logf("batch row=%d final_http=%d progress=%q error=%q", i, lastCode, lastState, lastError)
					}
				}()
				f.wait(t, "real per-row batch organization", func() bool {
					code, body, e := f.call(t, "GET", base, "", "", f.cookie, "")
					lastCode = code
					if e != nil {
						lastError = e.Error()
						return false
					}
					if e = json.Unmarshal(body, &view); e != nil {
						lastError = e.Error()
						return false
					}
					if len(view.Status) <= i || view.Status[i].Progress == nil {
						lastState = "no_progress"
						return false
					}
					lastState = view.Status[i].Progress.State
					return code == 200 && lastState == "completed"
				})
				completed++
				actual, e := os.ReadFile(filepath.Join(f.target, input.Rows[i].Confirmation.TargetRelativePath))
				if e != nil || !bytes.Equal(actual, []byte(fmt.Sprintf("owned batch bytes %d", i))) {
					t.Fatal("physical batch result", e)
				}
				a := m8UntrackedOrganizationIdentity(t, f.source, batch.Batch.Rows[i].File.RelativePath)
				b := m8UntrackedOrganizationIdentity(t, f.target, input.Rows[i].Confirmation.TargetRelativePath)
				if a.Device != b.Device || a.Inode != b.Inode {
					t.Fatal("not real hardlink")
				}
			}
			f.expect(t, "GET", f.path()+"/external-batch-commands/"+key, "", "", 200)
			f.count(t, `SELECT count(*) FROM untracked_organization_operation`, completed)
			f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, completed)
			f.count(t, `SELECT count(*) FROM download_task`, 0)
			f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, batch.Batch.ActorId)
			f.expect(t, "GET", base, "", "", 403)
			f.stop(t)
			t.Logf("MP28_BATCH_RUNTIME_PASS PG18.6 Cookie=REAL App=REAL hardlinks=%d stale_row=%t dropped_preview_response=1 dropped_org_responses=%d GET_recovery=no_resend", completed, stale, completed)
		})
	}
}
