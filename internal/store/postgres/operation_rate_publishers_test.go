package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func publisherSchema93(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if v, e := postgres.Migrate(t.Context(), pool.Config().ConnString(), repositoryMigrationsThrough(t, 93)); e != nil || v != 93 {
		t.Fatal("schema93", v, e)
	}
}

func publisherTelemetry(t *testing.T, pool *pgxpool.Pool, op uuid.UUID, progress float64, source, rate string) {
	t.Helper()
	got := rateRead(t, pool, op)[op]
	if got.Progress == nil || *got.Progress != progress || got.ProgressSource != source || got.ProgressObservedAt == nil || got.RateStatus != "current" || got.DownloadRateBytesPerSecond == nil || *got.DownloadRateBytesPerSecond != rate || got.RateObservedAt == nil {
		t.Fatalf("publisher telemetry: %+v", got)
	}
}

func publisherFrozen(t *testing.T, pool *pgxpool.Pool, query string, args ...any) string {
	t.Helper()
	var out string
	if e := pool.QueryRow(t.Context(), query, args...).Scan(&out); e != nil {
		t.Fatal(e)
	}
	return out
}

func publisherAutomaticTV(t *testing.T) *automaticTVObservationFixture {
	t.Helper()
	f := newAutomaticTVObservationFixture(t, true)
	publisherSchema93(t, f.pool)
	f.observer = postgres.NewAutomaticTVObservationStoreWithDownloadRates(f.pool, idgen.New())
	f.claim(t)
	return f
}

func TestOperationRatePublisherAutomaticTVPG(t *testing.T) {
	f := publisherAutomaticTV(t)
	s := f.load(t)
	frozen := publisherFrozen(t, f.pool, `SELECT jsonb_build_array((SELECT to_jsonb(a) FROM automatic_tv_execution_authority a WHERE operation_id=$1),(SELECT to_jsonb(p) FROM automatic_tv_submission_preparation p WHERE operation_id=$1))::text`, f.req.OperationID)
	before, _ := rateTask(t, f.pool, f.req.OperationID)
	remote := automaticTVObservationRemote(s, false)
	rate := int64(8192)
	remote.After[0].DownloadRateBytesPerSecond = &rate
	if out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *s, remote); e != nil || out.State != "tracking" || out.ReviewRequired {
		t.Fatal(out, e)
	}
	publisherTelemetry(t, f.pool, f.req.OperationID, 0.5, "verified_bytes", "8192")
	after, _ := rateTask(t, f.pool, f.req.OperationID)
	if after.Progress != before.Progress || after.DownloadedBytes != s.TotalBytes/2 || after.TotalBytes != s.TotalBytes {
		t.Fatal("autoTV must retain frozen percentage and write actual verified bytes", before, after)
	}
	if got := publisherFrozen(t, f.pool, `SELECT jsonb_build_array((SELECT to_jsonb(a) FROM automatic_tv_execution_authority a WHERE operation_id=$1),(SELECT to_jsonb(p) FROM automatic_tv_submission_preparation p WHERE operation_id=$1))::text`, f.req.OperationID); got != frozen {
		t.Fatal("autoTV frozen authority/preparation changed")
	}
}

// Build the same reviewed whole-set authority used by the HTTP regression,
// using creation/review/acquisition/preparation stores directly, without App.
func publisherCompleteTV(t *testing.T) (*pgxpool.Pool, *postgres.CompleteTVTrackingStore, ports.CompleteTVTrackingRequest) {
	t.Helper()
	f := newCreationFixture(t, 93)
	execIntent(t, f.pool, `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(f.actor.ID))
	f.actor.Role = ports.RoleAdmin
	encrypted, e := crypto.Encrypt(make([]byte, 32), []byte("publisher-password"))
	if e != nil {
		t.Fatal(e)
	}
	idx, client, candidate, query := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execIntent(t, f.pool, `INSERT INTO indexer(id,name,url,api_key_encrypted,health) VALUES($1,'publisher','https://publisher.invalid','fixture','online')`, idx)
	execIntent(t, f.pool, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'publisher','qbittorrent','https://publisher.invalid','fixture',$2,'{}')`, client, encrypted)
	in := creationInput(55102, "tv")
	in.IndexerIDs = []uuid.UUID{idx}
	in.DownloaderID = &client
	season := creationSeason(1, 1, 2)
	aired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range season.Episodes {
		season.Episodes[i].AirDate = &aired
	}
	metadata := creationTV(55102, season)
	metadata.Media.Title = "Show"
	year := int32(2020)
	metadata.Media.Year = &year
	created := f.create(t, in, metadata, "publisher-complete")
	execIntent(t, f.pool, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1`, f.tv)
	raw := []byte("d4:infod5:filesld6:lengthi512e4:pathl15:Show.S01E01.mkveed6:lengthi512e4:pathl15:Show.S01E02.mkveee4:name4:Show12:piece lengthi1024e6:pieces20:abcdefghijklmnopqrstee")
	proof, e := ports.InspectCompleteTVMetainfo(torrent.Inspector{}, raw)
	if e != nil {
		t.Fatal(e)
	}
	profile, _ := json.Marshal(quality.Profile{Resolution: "1080p", ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: "WEB-DL", SourceParserVersion: quality.SourceTokenParserVersion})
	execIntent(t, f.pool, `INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version,info_hash) SELECT $1,$2,'publisher','Show.2020.S01E01E02.1080p.WEB-DL.mkv','fixture',1024,$3,config_version,$4 FROM indexer WHERE id=$2`, candidate, idx, profile, proof.InfoHash())
	execIntent(t, f.pool, `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state,results_count,finished_at) SELECT $1,$2,'search','publisher',1,config_version,'succeeded',1,clock_timestamp() FROM indexer WHERE id=$2`, query, idx)
	execIntent(t, f.pool, `INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)`, query, candidate)
	mapping := postgres.NewEpisodeMappingStore(postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(f.pool, idgen.New())), idgen.New())
	reviews := subscription.NewTVCoverageCandidateReviewService(postgres.NewTVCoverageCandidateReviewStore(mapping))
	preview, e := reviews.Preview(t.Context(), f.actor, created.Subscription.ID, candidate)
	if e != nil || !preview.CanReview {
		t.Fatal("real whole-set preview", preview, e)
	}
	review, e := reviews.Review(t.Context(), f.actor, ports.TVCoverageCandidateReviewCommand{SubscriptionID: created.Subscription.ID, CandidateID: candidate, ExpectedInputStamp: preview.InputStamp, Confirmed: true, Reason: "Reviewed original whole set"}, "publisher-review")
	if e != nil {
		t.Fatal(e)
	}
	reserved, e := acquisition.NewCompleteTVService(postgres.NewCompleteTVAcquisitionStore(f.pool, idgen.New())).Acquire(t.Context(), f.actor, ports.AcquireCompleteTVCoverageCommand{SubscriptionID: created.Subscription.ID, ReviewID: review.ID, ExpectedInputStamp: preview.InputStamp, ExpectedMaterialStamp: preview.MaterialStamp, Confirmed: true}, "publisher-acquire")
	if e != nil {
		t.Fatal(e)
	}
	job, e := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"complete_tv_coverage_download_submission"}, Column2: "publisher-sender", Column3: 60000})
	if e != nil || job.ID != reserved.JobID {
		t.Fatal(job, e)
	}
	task := uuid.New()
	if _, e = postgres.NewCompleteTVSubmissionStore(f.pool, idgen.New()).PrepareCompleteTVSubmission(t.Context(), ports.PrepareCompleteTVSubmissionCommand{Lease: ports.JobLease{ID: job.ID, Owner: "publisher-sender", Token: job.LeaseToken}, TaskID: task, OperationID: reserved.OperationID, Metainfo: proof}); e != nil {
		t.Fatal("real preparation", e)
	}
	// As in newTrackingFixture, the original prepared task is now observed by
	// a distinct tracking job; no sender authority or historical receipt is forged.
	execIntent(t, f.pool, `UPDATE download_task SET state='tracking' WHERE id=$1`, task)
	req := ports.CompleteTVTrackingRequest{Lease: ports.JobLease{ID: uuid.New(), Owner: "publisher-tracker", Token: 7}, OperationID: reserved.OperationID, TaskID: task}
	execIntent(t, f.pool, `INSERT INTO job(id,kind,state,payload,lease_owner,lease_token,lease_until) VALUES($1,$2,'running',jsonb_build_object('task_id',$3::text,'operation_id',$4::text),$5,$6,clock_timestamp()+interval '1 minute')`, req.Lease.ID, ports.CompleteTVTrackingJobKind, task.String(), req.OperationID.String(), req.Lease.Owner, req.Lease.Token)
	// Manager creates and closes the genuine physical permit around load/commit.
	return f.pool, postgres.NewCompleteTVTrackingStoreWithDownloadRates(f.pool, idgen.New()), req
}

type publisherTrackingClient struct {
	ports.DownloadClient
	snapshot *ports.CompleteTVTrackingSnapshot
	progress float32
	rate     *int64
}

func (c *publisherTrackingClient) CompleteTVTrackingObserver() ports.CompleteTVTrackingClient {
	return c
}
func (c *publisherTrackingClient) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	s := c.snapshot
	return []*ports.TorrentInfo{{InfoHash: s.InfoHash, SavePath: s.SavePath, TotalBytes: s.TotalBytes, DownloadedBytes: s.TotalBytes / 2, Progress: c.progress, State: "downloading", Tags: s.OperationTag, DownloadRateBytesPerSecond: c.rate}}, nil
}
func (*publisherTrackingClient) GetTorrentFiles(context.Context, string) ([]ports.TorrentFile, error) {
	return nil, errors.New("partial observation must not request files")
}
func (*publisherTrackingClient) GetTorrentMetainfo(context.Context, string) ([]byte, error) {
	return nil, errors.New("partial observation must not request export")
}

func TestOperationRatePublisherCompleteTVPG(t *testing.T) {
	pool, store, req := publisherCompleteTV(t)
	task, _ := rateTask(t, pool, req.OperationID)
	client := &publisherTrackingClient{snapshot: &ports.CompleteTVTrackingSnapshot{InfoHash: task.InfoHash, SavePath: task.SavePath, TotalBytes: 1024, OperationTag: "pulsemedia:op:" + req.OperationID.String()}, progress: 0.625}
	rate := int64(4096)
	client.rate = &rate
	manager := download.NewManager(pool, idgen.New(), make([]byte, 32))
	// The native manager still owns the physical observation permit and budget.
	manager.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) { return client, nil })
	before := publisherFrozen(t, pool, `SELECT jsonb_build_array((SELECT to_jsonb(a) FROM complete_tv_execution_authority a WHERE operation_id=$1),(SELECT to_jsonb(p) FROM complete_tv_submission_preparation p WHERE operation_id=$1),(SELECT jsonb_agg(to_jsonb(u) ORDER BY unit_id) FROM complete_tv_submission_unit u WHERE task_id=$2))::text`, req.OperationID, req.TaskID)
	if out, e := manager.ObserveCompleteTVTracking(t.Context(), store, torrent.Inspector{}, req); e != nil || out.State != "tracking" || out.ReviewRequired {
		t.Fatal(out, e)
	}
	publisherTelemetry(t, pool, req.OperationID, 0.625, "native_percent", "4096")
	after, _ := rateTask(t, pool, req.OperationID)
	if after.Progress != task.Progress || after.TotalBytes != task.TotalBytes || after.DownloadedBytes != task.DownloadedBytes || !reflect.DeepEqual(after.FinishedAt, task.FinishedAt) {
		t.Fatal("native display changed protected task counters")
	}
	if got := publisherFrozen(t, pool, `SELECT jsonb_build_array((SELECT to_jsonb(a) FROM complete_tv_execution_authority a WHERE operation_id=$1),(SELECT to_jsonb(p) FROM complete_tv_submission_preparation p WHERE operation_id=$1),(SELECT jsonb_agg(to_jsonb(u) ORDER BY unit_id) FROM complete_tv_submission_unit u WHERE task_id=$2))::text`, req.OperationID, req.TaskID); got != before {
		t.Fatal("completeTV immutable footprint changed")
	}
}

// Add the native speed field to the fixture's real HTTP reply. The native
// driver still issues and consumes its private query proof normally.
type publisherRateTransport struct {
	rate     *int64
	progress *float64
}

func (p publisherRateTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, e := http.DefaultTransport.RoundTrip(r)
	if e != nil || r.URL.Path != "/api/v2/torrents/info" {
		return response, e
	}
	raw, e := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if e != nil {
		return nil, e
	}
	var rows []map[string]any
	if e = json.Unmarshal(raw, &rows); e != nil {
		return nil, e
	}
	for _, row := range rows {
		if p.rate != nil {
			row["dlspeed"] = *p.rate
		}
		if p.progress != nil {
			row["progress"] = *p.progress
		}
	}
	raw, e = json.Marshal(rows)
	if e != nil {
		return nil, e
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))
	response.Header.Del("Content-Length")
	return response, nil
}

func publisherUpgrade(t *testing.T) *trackingFixture {
	t.Helper()
	f := nativeTrackingFixture(t, "Ok.")
	publisherSchema93(t, f.p)
	store, e := postgres.NewUpgradePreparationStoreWithDownloadRates(f.p, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	f.repo, e = postgres.NewMovieUpgradeNativeTrackingRepository(store, f.driver, f.issuer)
	if e != nil {
		t.Fatal(e)
	}
	return f
}

func publisherUpgradeClient(f *trackingFixture, rate *int64, progress *float64) {
	f.manager.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
		return qbittorrent.NewClient(url, user, password, &http.Client{Transport: publisherRateTransport{rate: rate, progress: progress}})
	})
}

func TestOperationRatePublisherUpgradePG(t *testing.T) {
	for _, mode := range []string{"tracking", "zero", "nil_rate", "negative_rate", "completed", "manual_review"} {
		t.Run(mode, func(t *testing.T) {
			f := publisherUpgrade(t)
			f.complete.Store(mode == "completed")
			f.wrongHash.Store(mode == "manual_review")
			frozen := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			authority := publisherFrozen(t, f.p, `SELECT to_jsonb(a)::text FROM movie_upgrade_authority a WHERE operation_id=$1`, f.operation)
			oldReceipts := publisherFrozen(t, f.p, `SELECT coalesce(jsonb_agg(to_jsonb(i) ORDER BY idempotency_key),'[]')::text FROM idempotency_record i`)
			rate := int64(2048)
			progress := 0.5
			ratePtr := &rate
			switch mode {
			case "completed":
				progress = 1
			case "zero":
				rate = 0
				progress = 0
			case "nil_rate":
				ratePtr = nil
			case "negative_rate":
				rate = -1
			}
			publisherUpgradeClient(f, ratePtr, &progress)
			out, e := f.repo.TrackMovieUpgrade(t.Context(), f.actor, f.operation, uuid.New(), "publisher-admit", "publisher-result")
			want := "tracking"
			if mode == "completed" || mode == "manual_review" {
				want = mode
			}
			if e != nil || out.Outcome != want {
				t.Fatal(out, e)
			}
			got := rateRead(t, f.p, f.operation)[f.operation]
			if want == "tracking" {
				if got.Progress == nil || *got.Progress != progress || got.ProgressSource != "native_percent" {
					task, _ := rateTask(t, f.p, f.operation)
					t.Fatalf("native proof progress lost: telemetry=%+v frozen task state=%s", got, task.State)
				}
				if ratePtr != nil && rate >= 0 {
					publisherTelemetry(t, f.p, f.operation, progress, "native_percent", fmt.Sprint(rate))
				} else if got.RateStatus != "unknown" || got.DownloadRateBytesPerSecond != nil || got.RateObservedAt == nil {
					t.Fatal("unknown native rate revived old value", got)
				}
			} else {
				status := "waiting_organization"
				if want == "manual_review" {
					status = "review_required"
				}
				if detail := activityDetail(t, f.p, f.actor, f.operation); detail.Status != status {
					t.Fatal("activity ignored genuine tracking result", detail.Status, status)
				}
				if got.Progress != nil || got.DownloadRateBytesPerSecond != nil {
					t.Fatal("terminal observation retained tracking telemetry", got)
				}
			}
			receipt, _ := json.Marshal(out)
			replay, e := f.repo.TrackMovieUpgrade(t.Context(), f.actor, f.operation, uuid.New(), "publisher-admit", "publisher-result")
			encoded, _ := json.Marshal(replay)
			if e != nil || !bytes.Equal(receipt, encoded) {
				t.Fatal("historical receipt changed", replay, e)
			}
			if mode == "tracking" {
				task, stamp := rateTask(t, f.p, f.operation)
				for _, value := range []*float64{nil, func() *float64 { v := math.NaN(); return &v }(), func() *float64 { v := math.Inf(1); return &v }(), func() *float64 { v := 1.1; return &v }(), func() *float64 { v := -0.1; return &v }()} {
					tx, e := f.p.Begin(t.Context())
					if e != nil {
						t.Fatal(e)
					}
					e = postgres.RecordDownloadRate(t.Context(), tx, task, stamp, nil, "movie_upgrade", value)
					if e == nil {
						e = tx.Commit(t.Context())
					}
					_ = tx.Rollback(context.WithoutCancel(t.Context()))
					if e != nil {
						t.Fatal(e)
					}
					latest := rateRead(t, f.p, f.operation)[f.operation]
					if latest.Progress != nil || latest.ProgressSource != "" || latest.RateStatus != "unknown" || latest.DownloadRateBytesPerSecond != nil || latest.RateObservedAt == nil {
						t.Fatal("invalid latest revived frozen percentage or previous rate", latest)
					}
				}
			}
			if nativeProtectedGraph(t, f.preparedUpgradeFixture) != frozen || publisherFrozen(t, f.p, `SELECT to_jsonb(a)::text FROM movie_upgrade_authority a WHERE operation_id=$1`, f.operation) != authority {
				t.Fatal("upgrade changed original frozen task/preparation/hash/authority")
			}
			if old := publisherFrozen(t, f.p, `SELECT coalesce(jsonb_agg(to_jsonb(i) ORDER BY idempotency_key),'[]')::text FROM idempotency_record i WHERE idempotency_key NOT LIKE 'upgrade.tracking-%'`); old != oldReceipts {
				t.Fatal("old receipts changed")
			}
		})
	}
}

func TestOperationRatePublisherRollbackPG(t *testing.T) {
	for _, failure := range []string{"lease", "config", "source", "audit", "commit"} {
		t.Run(failure, func(t *testing.T) {
			if failure == "config" || failure == "source" {
				f := publisherUpgrade(t)
				f.complete.Store(false)
				rate := int64(123)
				publisherUpgradeClient(f, &rate, nil)
				frozen := nativeProtectedGraph(t, f.preparedUpgradeFixture)
				calls := 0
				hook := func() {
					calls++
					if calls != 1 {
						return
					}
					if failure == "config" {
						execIntent(t, f.p, `UPDATE download_client SET username='changed' WHERE id=$1`, f.client)
					} else {
						execIntent(t, f.p, `UPDATE indexer SET config_version=config_version+1 WHERE id=$1`, f.source)
					}
				}
				f.onInfo.Store(&hook)
				out, e := f.repo.TrackMovieUpgrade(t.Context(), f.actor, f.operation, uuid.New(), "held-admit", "held-result")
				if failure == "source" {
					// Tracking observes an existing downloader task. Changing the
					// indexer cannot revoke that read-only physical observation.
					if e != nil || out == nil || out.Outcome != "tracking" || calls == 0 {
						t.Fatal("indexer change interrupted existing task observation", out, e)
					}
					observed := rateRead(t, f.p, f.operation)[f.operation]
					if observed.DownloadRateBytesPerSecond == nil || *observed.DownloadRateBytesPerSecond != "123" || nativeProtectedGraph(t, f.preparedUpgradeFixture) != frozen {
						t.Fatal("indexer change invalidated downloader telemetry or frozen history", observed)
					}
					trackingCounts(t, f, 1, 1, 1)
					return
				}
				if e == nil || out != nil {
					t.Fatal("original current-evidence guard accepted held callback", out, e)
				}
				if calls == 0 {
					t.Fatal("native callback was not held")
				}
				trackingCounts(t, f, 1, 1, 0)
				if n := intentCount(t, f.p, `SELECT count(*) FROM download_rate_observation`); n != 0 {
					t.Fatal("stale callback retained sample", n)
				}
				if nativeProtectedGraph(t, f.preparedUpgradeFixture) != frozen {
					t.Fatal("stale callback changed frozen outcome")
				}
				return
			}

			f := publisherAutomaticTV(t)
			s := f.load(t)
			remote := automaticTVObservationRemote(s, false)
			rate := int64(123)
			remote.After[0].DownloadRateBytesPerSecond = &rate
			if failure == "lease" {
				execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.req.Lease.ID)
			}
			if failure == "audit" || failure == "commit" {
				execIntent(t, f.pool, `CREATE FUNCTION publisher_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'publisher test tail'; END $$`)
				if failure == "audit" {
					execIntent(t, f.pool, `CREATE TRIGGER publisher_failure BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION publisher_failure()`)
				} else {
					execIntent(t, f.pool, `CREATE CONSTRAINT TRIGGER publisher_failure AFTER INSERT OR UPDATE ON download_rate_observation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publisher_failure()`)
				}
			}
			before := activityGraph(t, f.pool)
			out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *s, remote)
			if e == nil || out != nil {
				t.Fatal("original guard accepted late callback", out, e)
			}
			if failure == "lease" && !errors.Is(e, ports.ErrAutomaticTVObservationStale) {
				t.Fatal("original lease guard", e)
			}
			assertEpisodeRecheckSnapshot(t, before, activityGraph(t, f.pool))
			if n := intentCount(t, f.pool, `SELECT count(*) FROM download_rate_observation`); n != 0 {
				t.Fatal("rolled back callback retained sample", n)
			}
			if failure == "audit" || failure == "commit" {
				table := "audit_log"
				if failure == "commit" {
					table = "download_rate_observation"
				}
				execIntent(t, f.pool, fmt.Sprintf("DROP TRIGGER publisher_failure ON %s", table))
				// Rollback retains the genuine job, lease token and physical permit.
				if out, e = f.observer.CommitAutomaticTVObservation(t.Context(), *s, remote); e != nil || out.State != "tracking" {
					t.Fatal("original job/token retry", out, e)
				}
				publisherTelemetry(t, f.pool, f.req.OperationID, 0.5, "verified_bytes", "123")
			}
		})
	}
}
