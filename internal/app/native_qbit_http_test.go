package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeQBHTTPFailureRetainsUnknownAndNeverReadds(t *testing.T) {
	for _, mode := range []string{"lost-ack", "service", "authentication", "rate", "withdraw"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dsn := pgtest.GetCluster(t).NewTestDB(t)
			if _, e := postgres.Migrate(ctx, dsn, "../../migrations"); e != nil {
				t.Fatal(e)
			}
			pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(pool.Close)
			q := postgres.Queries(pool)
			gen := idgen.New()
			key := []byte("12345678901234567890123456789012")
			rawTorrent, hash := ordinaryTestTorrent("Test.2020.mkv", 32)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(rawTorrent) }))
			defer origin.Close()
			sourceCredential, e := crypto.Encrypt(key, []byte("fixture"))
			if e != nil {
				t.Fatal(e)
			}
			rootPath := t.TempDir()
			var adds, deletes atomic.Int32
			var observe atomic.Bool
			var path atomic.Value
			var responseOverride atomic.Value
			responseOverride.Store("")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					if _, e := fmt.Fprint(w, "Ok."); e != nil {
						t.Error(e)
					}
				case "/api/v2/torrents/add":
					adds.Add(1)
					if e := r.ParseMultipartForm(1 << 20); e != nil {
						t.Error(e)
						return
					}
					if r.MultipartForm != nil {
						defer func() {
							if e := r.MultipartForm.RemoveAll(); e != nil {
								t.Error(e)
							}
						}()
					}
					if r.FormValue("urls") != "" {
						t.Error("URL delegated to downloader")
					}
					file, _, readErr := r.FormFile("torrents")
					if readErr != nil {
						t.Error(readErr)
						return
					}
					received, readErr := io.ReadAll(file)
					_ = file.Close()
					if readErr != nil || !bytes.Equal(received, rawTorrent) {
						t.Error("incorrect torrent payload", readErr)
					}
					path.Store(r.FormValue("savepath"))
					switch mode {
					case "lost-ack", "withdraw":
						h, ok := w.(http.Hijacker)
						if !ok {
							t.Error("no hijacker")
							return
						}
						conn, _, e := h.Hijack()
						if e != nil {
							t.Error(e)
							return
						}
						if e = conn.Close(); e != nil {
							t.Error(e)
						}
					case "authentication":
						w.WriteHeader(401)
					case "rate":
						w.Header().Set("Retry-After", "2")
						w.WriteHeader(429)
					default:
						w.WriteHeader(503)
					}
				case "/api/v2/torrents/delete":
					deletes.Add(1)
				case "/api/v2/torrents/info":
					if raw := responseOverride.Load().(string); raw != "" {
						if _, e := fmt.Fprint(w, raw); e != nil {
							t.Error(e)
						}
						return
					}
					if observe.Load() {
						if _, e := fmt.Fprintf(w, `[{"hash":%q,"save_path":%q,"content_path":%q,"progress":0.1,"size":32,"downloaded":3,"state":"downloading"}]`, hash, path.Load(), rootPath+"/Test.2020.mkv"); e != nil {
							t.Error(e)
						}
					} else {
						if _, e := fmt.Fprint(w, "[]"); e != nil {
							t.Error(e)
						}
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			idx, candidate, clientID, root, media, unit := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			if _, e = q.CreateIndexer(ctx, db.CreateIndexerParams{ID: idx, Name: "native", Url: origin.URL, ApiKeyEncrypted: sourceCredential, Categories: []byte("[]")}); e != nil {
				t.Fatal(e)
			}
			cipher, e := crypto.Encrypt(key, []byte(origin.URL+"/download?passkey=synthetic-qbit-secret"))
			if e != nil {
				t.Fatal(e)
			}
			profile := []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)
			if _, e = q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: idx, SourceConfigVersion: 1, Guid: "native", Title: "Test.2020.1080p.Bluray", LinkEncrypted: cipher, InfoHash: pgtype.Text{String: hash, Valid: true}, QualityProfile: profile}); e != nil {
				t.Fatal(e)
			}
			initialSource, e := q.GetCandidateByID(ctx, candidate)
			if e != nil {
				t.Fatal(e)
			}
			credential, e := crypto.Encrypt(key, []byte("synthetic-qbit-secret"))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: clientID, Name: "native", ClientType: "qbittorrent", Url: srv.URL, Username: "fixture", PasswordEncrypted: credential, RemotePathMap: []byte("{}")}); e != nil {
				t.Fatal(e)
			}
			if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Name: "movie", MediaType: "movie", Path: rootPath}); e != nil {
				t.Fatal(e)
			}
			if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Test", MediaType: "movie"}); e != nil {
				t.Fatal(e)
			}
			if _, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: media, UnitKey: "movie"}); e != nil {
				t.Fatal(e)
			}
			if _, e = pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", root); e != nil {
				t.Fatal(e)
			}
			actor := pgtest.CreateUserActor(t, q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
			result, e := acquisition.NewService(postgres.NewAcquisitionStore(pool, gen, clock.New(), acquisition.SelectMovieRound)).AcquireMovie(ctx, actor, acquisition.AcquireMovieCommand{RootID: root, UnitIDs: []uuid.UUID{unit}, CandidateID: candidate, DownloaderID: &clientID})
			if e != nil {
				t.Fatal(e)
			}
			factory := func(_ string, base, user, credential string) (ports.DownloadClient, error) {
				return qbittorrent.NewClient(base, user, credential, nil)
			}
			repo := downloadstore.NewRepository(pool, gen, key)
			repo.SetTorrentSource(ordinaryTestSource, torrent.Inspector{})
			manager := download.NewManager(repo)
			manager.SetSubmissionRepository(postgres.NewDownloadSubmissionStore(pool, gen))
			manager.SetClientFactory(factory)
			job, e := jobs.NewQueue(pool, gen, clock.New()).Claim(ctx, []string{"download_submission"}, "native-http", 5*time.Minute)
			if e != nil || job == nil {
				t.Fatal(job, e)
			}
			if _, e = handleDownloadJob(ctx, q, manager, job, clientID); e != nil {
				t.Fatal(e)
			}
			original, e := q.GetDownloadTaskByOpID(ctx, result.OperationID)
			if e != nil || original.State != "unknown" || adds.Load() != 1 {
				t.Fatalf("submission should remain unknown: %s %v adds=%d", original.State, e, adds.Load())
			}
			reconstructed := download.NewManager(downloadstore.NewRepository(pool, gen, key))
			reconstructed.SetClientFactory(factory)
			if _, e = handleDownloadJob(ctx, q, reconstructed, job, clientID); e != nil {
				t.Fatal(e)
			}
			pending, e := q.GetDownloadTaskByOpID(ctx, result.OperationID)
			if e != nil || pending.State != "unknown" || adds.Load() != 1 {
				t.Fatal("empty observation released/replayed unknown", e)
			}
			var protections int
			if e = pool.QueryRow(ctx, "SELECT count(*) FROM unit_reservation WHERE operation_id=$1", result.OperationID).Scan(&protections); e != nil || protections != 1 {
				t.Fatal("reservation changed", e)
			}
			if mode == "withdraw" {
				withdraw := acquisition.NewWithdrawalService(postgres.NewDownloadSubmissionStore(pool, gen))
				own, e := withdraw.List(ctx, actor, result.OperationID)
				if e != nil || len(own) != 1 {
					t.Fatal(own, e)
				}
				command := ports.RequirementWithdrawalCommand{OperationID: result.OperationID, RequirementID: own[0].ID, ExpectedFence: own[0].FenceDigest, Key: "withdraw-native"}
				for range 2 {
					receipt, e := withdraw.Withdraw(ctx, actor, command)
					if e != nil || receipt.PhysicalReleased {
						t.Fatal("unknown physically cancelled", receipt, e)
					}
				}
				after, e := q.GetDownloadTaskByOpID(ctx, result.OperationID)
				if e != nil || after.State != "unknown" || after.ID != original.ID || adds.Load() != 1 || deletes.Load() != 0 {
					t.Fatal("withdraw replayed or deleted physical operation", e)
				}
				var protected int
				if e = pool.QueryRow(ctx, "SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND state IN ('reserved','dispatched')", result.OperationID).Scan(&protected); e != nil || protected != 1 {
					t.Fatal("unknown protection released", protected, e)
				}
				rows, e := q.ListRequirementsForOperation(ctx, result.OperationID)
				if e != nil || len(rows) != 1 || rows[0].State != "released" {
					t.Fatal("logical withdrawal not persisted", rows, e)
				}
				return
			}
			validRow := fmt.Sprintf(`{"hash":%q,"save_path":%q,"content_path":%q,"progress":0.1,"size":32,"downloaded":3,"state":"downloading"}`, hash, rootPath, rootPath+"/Test.2020.mkv")
			for _, raw := range []string{
				`[` + strings.Replace(validRow, hash, strings.Repeat("b", 40), 1) + `]`,
				`[` + validRow + `,` + validRow + `]`,
				`[` + strings.Replace(validRow, `"downloaded":3,`, "", 1) + `]`,
				`[` + strings.Replace(validRow, `"size":32`, `"size":null`, 1) + `]`,
			} {
				responseOverride.Store(raw)
				if _, e = reconstructed.ReconcileTask(ctx, result.OperationID); !errors.Is(e, ports.ErrBadResponse) {
					t.Fatalf("invalid observation accepted: %v", e)
				}
				if _, e = handleDownloadJob(ctx, q, reconstructed, job, clientID); e != nil {
					t.Fatal(e)
				}

				unchanged, err := q.GetDownloadTaskByOpID(ctx, result.OperationID)
				if err != nil || unchanged.State != "unknown" || unchanged.ID != original.ID || adds.Load() != 1 {
					t.Fatal("invalid response changed unknown", err)
				}
				if err = pool.QueryRow(ctx, "SELECT count(*) FROM unit_reservation WHERE operation_id=$1", result.OperationID).Scan(&protections); err != nil || protections != 1 {
					t.Fatal("invalid observation released reservation", err)
				}
			}
			responseOverride.Store("")
			observe.Store(true)
			if _, e = handleDownloadJob(ctx, q, reconstructed, job, clientID); e != nil {
				t.Fatal(e)
			}
			tracked, e := q.GetDownloadTaskByOpID(ctx, result.OperationID)
			if e != nil || tracked.State != "tracking" || tracked.ID != original.ID || adds.Load() != 1 {
				t.Fatalf("reconcile: %s %v adds=%d", tracked.State, e, adds.Load())
			}
			var jobsBefore int
			if e = pool.QueryRow(ctx, "SELECT count(*) FROM job").Scan(&jobsBefore); e != nil {
				t.Fatal(e)
			}
			responseOverride.Store(fmt.Sprintf(`[{"hash":%q,"save_path":%q,"content_path":%q,"progress":0.999999999999999999999,"size":32,"downloaded":32,"state":"uploading"}]`, hash, rootPath, rootPath+"/Test.2020.mkv"))
			if _, e = reconstructed.ObserveTask(ctx, result.OperationID); e != nil {
				t.Fatal(e)
			}
			partial, e := q.GetDownloadTaskByOpID(ctx, result.OperationID)
			if e != nil || partial.State != "tracking" || partial.Progress >= 1 {
				t.Fatal("partial progress completed", e)
			}
			var organizations int
			if e = pool.QueryRow(ctx, "SELECT count(*) FROM job").Scan(&organizations); e != nil || organizations != jobsBefore {
				t.Fatal("premature organization job", e)
			}
			stored, e := q.GetCandidateByID(ctx, candidate)
			if e != nil || string(stored.LinkEncrypted) != string(initialSource.LinkEncrypted) || string(stored.QualityProfile) != string(initialSource.QualityProfile) {
				t.Fatal("source rewritten", e)
			}
		})
	}
}
