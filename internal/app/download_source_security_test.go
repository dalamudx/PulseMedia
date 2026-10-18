package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func ordinaryTestTorrent(name string, size int) ([]byte, string) {
	info := fmt.Sprintf("d6:lengthi%de4:name%d:%s12:piece lengthi%de6:pieces20:00000000000000000000e", size, len(name), name, size)
	hash := sha1.Sum([]byte(info))
	return []byte("d4:info" + info + "e"), hex.EncodeToString(hash[:])
}

func assertOrdinaryTorrentUpload(t *testing.T, r *http.Request, raw []byte) {
	t.Helper()
	if r.FormValue("urls") != "" {
		t.Error("HTTP URL delegated to downloader")
	}
	file, _, err := r.FormFile("torrents")
	if err != nil {
		t.Error(err)
		return
	}
	defer func() { _ = file.Close() }()
	got, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(got, raw) {
		t.Error("incorrect torrent payload", err)
	}
}
func ordinaryTestSource(_, endpoint, credential string) ports.IndexerClient {
	return torznab.NewClient(endpoint, credential, nil)
}

func TestOrdinarySubmissionSourceSecurityPG(t *testing.T) {
	for _, mode := range []string{"same-origin", "missing-hash", "cross-origin", "redirect", "oversize", "invalid-metainfo", "hash-mismatch", "config-changed", "expired-lease", "missing-factory", "magnet", "magnet-http-source"} {
		t.Run(mode, func(t *testing.T) {
			f := organizationSetup(t)
			ctx := context.Background()
			if _, err := f.pool.Exec(ctx, "DELETE FROM download_task;DELETE FROM unit_reservation;DELETE FROM download_requirement;DELETE FROM release_candidate;DELETE FROM indexer;"); err != nil {
				t.Fatal(err)
			}
			raw, hash := ordinaryTestTorrent("Movie.2020.mkv", 32)
			var sourceHits, probeHits, adds atomic.Int32
			probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { probeHits.Add(1) }))
			defer probe.Close()
			var claimed *db.Job
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceHits.Add(1)
				switch mode {
				case "redirect":
					http.Redirect(w, r, probe.URL+"/probe", http.StatusFound)
				case "oversize":
					_, _ = w.Write(bytes.Repeat([]byte{'x'}, ports.MaxTorrentMetainfoBytes+1))
				case "invalid-metainfo":
					_, _ = io.WriteString(w, "not a torrent")
				default:
					if mode == "config-changed" {
						if _, err := f.pool.Exec(ctx, "UPDATE indexer SET config_version=config_version+1"); err != nil {
							t.Error(err)
						}
					}
					if mode == "expired-lease" {
						if _, err := f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", claimed.ID); err != nil {
							t.Error(err)
						}
					}
					_, _ = w.Write(raw)
				}
			}))
			defer origin.Close()
			qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = io.WriteString(w, "Ok.")
				case "/api/v2/torrents/add":
					adds.Add(1)
					if err := r.ParseMultipartForm(ports.MaxTorrentMetainfoBytes); err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = r.MultipartForm.RemoveAll() }()
					if mode == "magnet" {
						if r.FormValue("urls") != "magnet:?xt=urn:btih:"+hash {
							t.Error("magnet changed")
						}
					} else {
						if r.FormValue("urls") != "" {
							t.Error("HTTP URL delegated to downloader")
						}
						file, _, err := r.FormFile("torrents")
						if err != nil {
							t.Error(err)
							return
						}
						defer func() { _ = file.Close() }()
						got, err := io.ReadAll(file)
						if err != nil || !bytes.Equal(got, raw) {
							t.Error("exact torrent bytes not sent", err)
						}
					}
					_, _ = io.WriteString(w, "Ok.")
				default:
					http.NotFound(w, r)
				}
			}))
			defer qb.Close()
			key := make([]byte, 32)
			credential, err := crypto.Encrypt(key, []byte("synthetic-key"))
			if err != nil {
				t.Fatal(err)
			}
			idx, err := f.q.CreateIndexer(ctx, db.CreateIndexerParams{ID: uuid.New(), Name: "source", Url: origin.URL, ApiKeyEncrypted: credential, Categories: []byte("[]")})
			if err != nil {
				t.Fatal(err)
			}
			link := origin.URL + "/torrent?passkey=synthetic-secret"
			if mode == "cross-origin" {
				link = probe.URL + "/probe?passkey=synthetic-secret"
			}
			if strings.HasPrefix(mode, "magnet") {
				link = "magnet:?xt=urn:btih:" + hash
			}
			if mode == "magnet-http-source" {
				link += "&xs=" + probe.URL + "/probe"
			}
			encrypted, err := crypto.Encrypt(key, []byte(link))
			if err != nil {
				t.Fatal(err)
			}
			candidateHash := pgtype.Text{String: hash, Valid: true}
			if mode == "missing-hash" {
				candidateHash = pgtype.Text{}
			}
			if mode == "hash-mismatch" {
				candidateHash.String = strings.Repeat("b", 40)
			}
			candidate, err := f.q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: uuid.New(), IndexerID: idx.ID, SourceConfigVersion: 1, Guid: "security", Title: "Movie.2020.1080p.BluRay", LinkEncrypted: encrypted, InfoHash: candidateHash, QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)})
			if err != nil {
				t.Fatal(err)
			}
			actor := pgtest.CreateUserActor(t, f.q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
			result, err := acquisition.NewService(postgres.NewAcquisitionStore(f.pool, f.gen, clock.New(), acquisition.SelectMovieRound)).AcquireMovie(ctx, actor, acquisition.AcquireMovieCommand{RootID: f.root, UnitIDs: []uuid.UUID{f.unit}, CandidateID: candidate.ID, DownloaderID: &f.client})
			if err != nil {
				t.Fatal(err)
			}
			claimed, err = jobs.NewQueue(f.pool, f.gen, clock.New()).Claim(ctx, []string{"download_submission"}, "source-security", time.Minute)
			if err != nil || claimed == nil {
				t.Fatal(claimed, err)
			}
			repo := downloadstore.NewRepository(f.pool, f.gen, key)
			if mode != "missing-factory" {
				repo.SetTorrentSource(ordinaryTestSource, torrent.Inspector{})
			}
			mgr := download.NewManager(repo)
			mgr.SetSubmissionRepository(postgres.NewDownloadSubmissionStore(f.pool, f.gen))
			client, err := qbittorrent.NewClient(qb.URL, "fixture", "fixture", nil)
			if err != nil {
				t.Fatal(err)
			}
			mgr.RegisterClient(f.client, client)
			_, err = handleDownloadJob(ctx, f.q, mgr, claimed, f.client)
			valid := mode == "same-origin" || mode == "missing-hash" || mode == "magnet"
			if valid {
				task, readErr := f.q.GetDownloadTaskByOpID(ctx, result.OperationID)
				if err != nil || readErr != nil || task.State != "tracking" || task.InfoHash != hash || adds.Load() != 1 {
					t.Fatalf("valid submit: state=%s adds=%d err=%v read=%v", task.State, adds.Load(), err, readErr)
				}
				if _, err = handleDownloadJob(ctx, f.q, mgr, claimed, f.client); err != nil || adds.Load() != 1 {
					t.Fatal("same operation replay", err)
				}
			} else {
				if err == nil || adds.Load() != 0 {
					t.Fatalf("unsafe source admitted: adds=%d err=%v", adds.Load(), err)
				}
				if strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), origin.URL) {
					t.Fatal("secret URL in error")
				}
				if _, err = f.q.GetDownloadTaskByOpID(ctx, result.OperationID); !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal("source failure persisted submitting task", err)
				}
				var protections int
				if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL", result.OperationID).Scan(&protections); err != nil || protections != 1 {
					t.Fatal("source failure released protection", protections, err)
				}
			}
			if probeHits.Load() != 0 {
				t.Fatal("untrusted destination reached", probeHits.Load())
			}
			noFetch := mode == "cross-origin" || mode == "missing-factory" || strings.HasPrefix(mode, "magnet")
			if noFetch && sourceHits.Load() != 0 {
				t.Fatal("unissued source fetched", sourceHits.Load())
			}
		})
	}
}
