package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestPublicMovieUpgradeWorkflowBrowser(t *testing.T) {
	if os.Getenv("PULSE_UPGRADE_UI_BROWSER") != "1" {
		t.Skip("explicit disposable Chrome fixture required")
	}
	dist := os.Getenv("PULSE_UPGRADE_UI_DIST")
	if dist == "" {
		t.Fatal("owned frontend build required")
	}
	if _, e := os.Stat(filepath.Join(dist, "index.html")); e != nil {
		t.Fatal(e)
	}
	cutoff := 1000
	switch os.Getenv("PULSE_UPGRADE_UI_MODE") {
	case "", string(ports.UpgradeStopAtCutoff):
	case string(ports.UpgradeReplaceIfBetter):
		cutoff = 300
	default:
		t.Fatal("unsupported browser fixture mode")
	}
	f := newPublicUpgradeFixtureWithCutoff(t, cutoff)
	preparePublicUpgradePhysicalFiles(t, f)
	var remoteRoot string
	if e := f.a.DBPool().QueryRow(context.Background(), `SELECT path FROM library_root WHERE id=$1`, f.root).Scan(&remoteRoot); e != nil {
		t.Fatal(e)
	}
	originalPath := filepath.Join(remoteRoot, "original.mkv")
	originalStat, e := os.Stat(originalPath)
	if e != nil {
		t.Fatal(e)
	}
	seedPath := filepath.Join(remoteRoot, "kept-seeding-link.mkv")
	if e = os.Link(originalPath, seedPath); e != nil {
		t.Fatal(e)
	}
	master, e := crypto.EnsureMasterKey(f.a.Config().DataDir, f.a.Config().SecretKey)
	if e != nil {
		t.Fatal(e)
	}
	name := "Fight.Club.1999.2160p.Bluray.x265.mkv"
	info := fmt.Sprintf("d6:lengthi120e4:name%d:%s12:piece lengthi16384e6:pieces20:01234567890123456789e", len(name), name)
	raw := []byte("d4:info" + info + "e")
	hash := sha1.Sum([]byte(info))
	var fetches, adds atomic.Int32
	var nativeTags atomic.Value
	nativeTags.Store("")
	var badUpload atomic.Bool
	var sourceURL string
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/torrent" {
			if r.URL.Query().Get("apikey") != "upgrade-source-key" {
				http.NotFound(w, r)
				return
			}
			fetches.Add(1)
			w.Header().Set("Content-Type", "application/x-bittorrent")
			if _, err := w.Write(raw); err != nil {
				t.Error(err)
			}
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		rss := fmt.Sprintf(`<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="1"/><item><title>Fight.Club.1999.2160p.Bluray.x265</title><guid>native-upgrade-ui-release</guid><size>120</size><enclosure url="%s/torrent?apikey=upgrade-source-key" length="120" type="application/x-bittorrent"/><torznab:attr name="infohash" value="%s"/><torznab:attr name="seeders" value="10"/><torznab:attr name="category" value="2000"/></item></channel></rss>`, sourceURL, hex.EncodeToString(hash[:]))
		if _, err := io.WriteString(w, rss); err != nil {
			t.Error(err)
		}
	}))
	defer source.Close()
	sourceURL = source.URL
	downloader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "upgrade-ui-session", Path: "/"})
			if _, err := io.WriteString(w, "Ok."); err != nil {
				t.Error(err)
			}
		case "/api/v2/torrents/info":
			if adds.Load() == 0 {
				_, _ = io.WriteString(w, "[]")
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": hex.EncodeToString(hash[:]), "name": name, "tags": nativeTags.Load(), "state": "uploading", "progress": 1, "size": 120, "downloaded": 120, "amount_left": 0, "save_path": remoteRoot, "content_path": filepath.Join(remoteRoot, name)}})
			}
		case "/api/v2/torrents/files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"index": 0, "name": name, "size": 120, "progress": 1, "priority": 1}})
		case "/api/v2/torrents/export":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write(raw)
		case "/api/v2/torrents/add":
			adds.Add(1)
			if err := r.ParseMultipartForm(ports.MaxTorrentMetainfoBytes); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer func() {
				if err := r.MultipartForm.RemoveAll(); err != nil {
					t.Error(err)
				}
			}()
			file, _, err := r.FormFile("torrents")
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			payload, err := io.ReadAll(io.LimitReader(file, ports.MaxTorrentMetainfoBytes+1))
			if closeErr := file.Close(); closeErr != nil {
				t.Error(closeErr)
			}
			if err != nil || !bytes.Equal(payload, raw) || r.FormValue("savepath") == "" || r.FormValue("tags") == "" {
				badUpload.Store(true)
			}
			nativeTags.Store(r.FormValue("tags"))
			if _, err = io.WriteString(w, "Ok."); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer downloader.Close()
	password, e := crypto.Encrypt(master, []byte("upgrade-ui-password"))
	if e != nil {
		t.Fatal(e)
	}
	key, e := crypto.Encrypt(master, []byte("upgrade-source-key"))
	if e != nil {
		t.Fatal(e)
	}
	link, e := crypto.Encrypt(master, []byte(source.URL+"/torrent?apikey=upgrade-source-key"))
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE download_client SET url=$2,username='native-admin',password_encrypted=$3 WHERE id=$1", f.client, downloader.URL, password)
	f.exec(t, "UPDATE indexer SET url=$2,api_key_encrypted=$3,health='online',health_next_check_at=clock_timestamp()+interval '1 hour' WHERE id=$1", f.source, source.URL, key)
	f.exec(t, "UPDATE release_candidate SET info_hash=$2,link_encrypted=$3 WHERE id=$1", f.request.CandidateID, hex.EncodeToString(hash[:]), link)
	f.exec(t, "INSERT INTO media_identity(id,media_id,namespace,external_id) SELECT $1,media_id,'tmdb.movie','550' FROM subscription WHERE id=$2", uuid.New(), f.request.SubscriptionID)
	start := publicUpgradeAppStarter(t, f)
	stop := start()
	defer stop()
	var before string
	const originalQuery = `SELECT jsonb_build_object('file',(SELECT to_jsonb(lf) FROM library_file lf WHERE id=$1),'mapping',(SELECT jsonb_agg(to_jsonb(fmu) ORDER BY fmu.unit_id) FROM library_file_unit fmu WHERE file_id=$1),'facts',(SELECT jsonb_agg(to_jsonb(uf) ORDER BY uf.id) FROM unit_fact uf WHERE source_file_id=$1))::text`
	if err := f.a.DBPool().QueryRow(context.Background(), originalQuery, f.request.CurrentFileID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	target, e := url.Parse(f.runtimeURL)
	if e != nil {
		t.Fatal(e)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	control := "/__upgrade_ui_" + f.request.SubscriptionID.String()
	ui := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == control+"/facts" {
			cookie, err := r.Cookie("pulsemedia_session")
			if r.Method != "GET" || err != nil || cookie.Value != f.admin {
				http.Error(w, "fixture access denied", http.StatusForbidden)
				return
			}
			var tasks, commands, prepared, returned, linked, ingested, retired int
			var after string
			err = f.a.DBPool().QueryRow(r.Context(), `SELECT (SELECT count(*) FROM download_task),(SELECT count(*) FROM movie_upgrade_send_job),(SELECT count(*) FROM movie_upgrade_preparation),(SELECT count(*) FROM movie_upgrade_send_result),(SELECT count(*) FROM movie_upgrade_organization_execution_result WHERE status='linked_pending_ingestion'),(SELECT count(*) FROM movie_upgrade_organization_ingestion),(SELECT count(*) FROM movie_upgrade_retirement_result WHERE status='retired')`).Scan(&tasks, &commands, &prepared, &returned, &linked, &ingested, &retired)
			if err == nil {
				err = f.a.DBPool().QueryRow(r.Context(), originalQuery, f.request.CurrentFileID).Scan(&after)
			}
			if err != nil {
				http.Error(w, "fixture facts unavailable", 500)
				return
			}
			hardlinked := false
			if ingested == 1 {
				var sourcePath, targetPath string
				if e := f.a.DBPool().QueryRow(r.Context(), `SELECT (SELECT value FROM download_client c,jsonb_each_text(c.remote_path_map) WHERE c.id=$1 LIMIT 1)||'/'||$2,root.path||'/'||file.rel_path FROM movie_upgrade_organization_ingestion i JOIN library_file file ON file.id=i.file_id JOIN library_root root ON root.id=file.root_id`, f.client, name).Scan(&sourcePath, &targetPath); e != nil {
					http.Error(w, "fixture file unavailable", 500)
					return
				}
				sourceStat, se := os.Stat(sourcePath)
				targetStat, te := os.Stat(targetPath)
				hardlinked = se == nil && te == nil && os.SameFile(sourceStat, targetStat)
			}
			recycled, oldMissing := false, false
			seedInfo, seedErr := os.Stat(seedPath)
			seedPreserved := seedErr == nil && os.SameFile(originalStat, seedInfo)
			if retired == 1 {
				var relative string
				if e := f.a.DBPool().QueryRow(r.Context(), `SELECT recycle_rel FROM movie_upgrade_retirement_command`).Scan(&relative); e != nil {
					http.Error(w, "fixture recycle unavailable", 500)
					return
				}
				recycledInfo, recycledErr := os.Stat(filepath.Join(remoteRoot, relative))
				recycled = recycledErr == nil && os.SameFile(originalStat, recycledInfo)
				_, oldErr := os.Lstat(originalPath)
				oldMissing = os.IsNotExist(oldErr)
			}
			w.Header().Set("Content-Type", "application/json")
			if err = json.NewEncoder(w).Encode(map[string]any{"tasks": tasks, "commands": commands, "prepared": prepared, "returned": returned, "fetches": fetches.Load(), "adds": adds.Load(), "original_unchanged": before == after, "linked": linked, "ingested": ingested, "hardlinked": hardlinked, "retired": retired, "recycled": recycled, "old_path_missing": oldMissing, "seed_preserved": seedPreserved}); err != nil {
				t.Error(err)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" || !strings.Contains(filepath.Base(r.URL.Path), ".") {
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			return
		}
		http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
	}))
	defer ui.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()
	script, e := filepath.Abs("../../web/tests/movie-upgrade-browser.mjs")
	if e != nil {
		t.Fatal(e)
	}
	command := exec.CommandContext(ctx, "node", script)
	command.Env = append(os.Environ(), "PULSE_UPGRADE_UI_BASE="+ui.URL, "PULSE_UPGRADE_UI_SUB="+f.request.SubscriptionID.String(), "PULSE_UPGRADE_UI_SESSION="+f.admin, "PULSE_UPGRADE_UI_CONTROL="+control, "PULSE_UPGRADE_UI_FILE="+f.request.CurrentFileID.String(), "PULSE_UPGRADE_UI_USER="+f.actor.ID)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("native upgrade browser failed: %v\n%s", err, output.String())
	}
	t.Log(output.String())
	if fetches.Load() != 2 || adds.Load() != 1 || badUpload.Load() {
		t.Fatalf("native counts: fetch=%d add=%d exact=%t", fetches.Load(), adds.Load(), !badUpload.Load())
	}
	var results, jobs int
	if err := f.a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM movie_upgrade_send_result WHERE status='confirmed'),(SELECT count(*) FROM job j JOIN movie_upgrade_send_job b ON b.job_id=j.id JOIN movie_upgrade_send_result r ON r.operation_id=b.operation_id WHERE j.state='succeeded' AND r.status='confirmed')`).Scan(&results, &jobs); err != nil {
		t.Fatal(err)
	}
	if results != 1 || jobs != 1 {
		t.Fatalf("atomic return/job=%d/%d", results, jobs)
	}
	var completed bool
	if e := f.a.DBPool().QueryRow(ctx, `SELECT oldfile.retired_at=r.recorded_at AND oldfact.revoked_at=r.recorded_at AND newfile.retired_at IS NULL AND newfact.revoked_at IS NULL AND lane.released_at=r.recorded_at AND reservation.state='consumed' AND requirement.state='imported' FROM movie_upgrade_authority a JOIN movie_upgrade_organization_ingestion i USING(operation_id) JOIN movie_upgrade_retirement_result r USING(operation_id) JOIN library_file oldfile ON oldfile.id=a.old_file_id JOIN library_file newfile ON newfile.id=i.file_id JOIN unit_fact oldfact ON oldfact.id=a.old_fact_id JOIN unit_fact newfact ON newfact.id=i.fact_id JOIN acquisition_reservation lane ON lane.operation_id=a.operation_id JOIN unit_reservation reservation ON reservation.operation_id=a.operation_id JOIN download_requirement requirement ON requirement.operation_id=a.operation_id WHERE r.status='retired'`).Scan(&completed); e != nil || !completed {
		t.Fatal("browser retirement effects", completed, e)
	}
}
