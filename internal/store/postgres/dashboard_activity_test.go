package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type dashboardActivityFixture struct {
	creationFixture
	store                   *postgres.DashboardActivityStore
	unit, candidate, client uuid.UUID
}

func newDashboardActivityFixture(t *testing.T, through ...int64) dashboardActivityFixture {
	t.Helper()
	base := newCreationFixture(t, through...)
	f := dashboardActivityFixture{creationFixture: base, store: postgres.NewDashboardActivityStore(base.pool), unit: uuid.New(), candidate: uuid.New(), client: uuid.New()}
	media, source := uuid.New(), uuid.New()
	execIntent(t, f.pool, `INSERT INTO media(id,title,media_type) VALUES($1,'Activity movie','movie')`, media)
	execIntent(t, f.pool, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')`, f.unit, media)
	execIntent(t, f.pool, `INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'activity','https://activity.invalid','fixture')`, source)
	execIntent(t, f.pool, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes) VALUES($1,$2,'activity','activity','fixture',100)`, f.candidate, source)
	execIntent(t, f.pool, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted) VALUES($1,'activity','qbittorrent','https://activity.invalid','fixture','fixture')`, f.client)
	return f
}
func (f dashboardActivityFixture) add(t *testing.T, actor string, state string, at time.Time) uuid.UUID {
	t.Helper()
	op := uuid.New()
	execIntent(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,created_at) VALUES($1,$2,$3,$4,'user',$5,$6)`, uuid.New(), op, f.unit, f.movie, actor, at)
	execIntent(t, f.pool, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path,content_path,progress,total_bytes,downloaded_bytes,updated_at) VALUES($1,$2,$3,$4,$5,$6,'/downloads','/downloads/item.mkv',0.25,100,500,$7)`, uuid.New(), op, f.client, f.candidate, strings.Repeat("a", 40), state, at)
	return op
}
func dashboardActivityRead(t *testing.T, pool *pgxpool.Pool, actor *ports.Actor, limit int) *ports.DashboardActivityPage {
	t.Helper()
	page, err := postgres.NewDashboardActivityStore(pool).ReadDashboardActivity(t.Context(), actor, limit)
	if err != nil || page == nil {
		t.Fatalf("activity: %+v %v", page, err)
	}
	return page
}
func dashboardActivityGraph(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	graph := episodeRecheckSnapshot(t, pool, uuid.Nil, false)
	for _, table := range []string{"automatic_tv_execution_authority", "automatic_tv_submission_preparation", "automatic_tv_submission_result", "automatic_tv_physical_attempt", "automatic_tv_observation_job", "automatic_tv_observation_result", "automatic_tv_observation_permit", "transfer_plan", "library_file", "library_file_unit"} {
		var value string
		if err := pool.QueryRow(t.Context(), "SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM "+table+" r").Scan(&value); err != nil {
			t.Fatal(table, err)
		}
		graph[table] = value
	}
	return graph
}
func TestDashboardActivityVisibilityBoundsPG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	foreign := uuid.NewString()
	own := make([]uuid.UUID, 15)
	for i := range own {
		own[i] = f.add(t, f.actor.ID, "tracking", at)
	}
	hidden := f.add(t, foreign, "tracking", at.Add(time.Hour))
	execIntent(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,created_at) VALUES($1,$2,$3,$4,'user',$5,$6)`, uuid.New(), own[0], f.unit, f.movie, foreign, at)
	sort.Slice(own, func(i, j int) bool { return own[i].String() > own[j].String() })
	before := dashboardActivityGraph(t, f.pool)
	for _, limit := range []int{0, 1, 6, 12} {
		n := limit
		if n == 0 {
			n = 6
		}
		page := dashboardActivityRead(t, f.pool, f.actor, limit)
		if len(page.Items) != n || !page.More || page.ObservedAt.IsZero() {
			t.Fatalf("bounds %d: %+v", limit, page)
		}
		for i, item := range page.Items {
			if item.OperationID != own[i] || item.Status != "tracking" || item.Title != "Activity movie" || item.Progress == nil || *item.Progress != 0.25 || item.ProgressSource != "native_percent" || item.UpdatedAt == nil || !item.UpdatedAt.Equal(at) {
				t.Fatalf("item %d: %+v", i, item)
			}
		}
		again := dashboardActivityRead(t, f.pool, f.actor, limit)
		if !reflect.DeepEqual(page.Items, again.Items) {
			t.Fatal("unstable order")
		}
	}
	admin := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin, Scopes: []string{"read"}}
	execIntent(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'activity-admin','fixture','admin')`, uuid.MustParse(admin.ID))
	page := dashboardActivityRead(t, f.pool, admin, 12)
	if len(page.Items) != 12 || !page.More || page.Items[0].OperationID != hidden {
		t.Fatalf("admin visibility %+v", page)
	}
	assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
}
func TestDashboardActivityAuthorizationPG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	for _, tc := range []struct {
		name  string
		actor *ports.Actor
		limit int
		want  error
	}{
		{"nil", nil, 0, ports.ErrUnauthorized},
		{"empty scopes", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleMember, Scopes: []string{}}, 0, ports.ErrForbidden},
		{"write scope", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleMember, Scopes: []string{"write"}}, 0, ports.ErrForbidden},
		{"forged admin", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleAdmin}, 0, ports.ErrForbidden},
		{"missing user", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}, 0, ports.ErrForbidden},
		{"bad id", &ports.Actor{Type: ports.ActorTypeUser, ID: "invalid", Role: ports.RoleMember}, 0, ports.ErrForbidden},
		{"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: f.actor.ID, Role: ports.RoleAdmin}, 0, ports.ErrForbidden},
		{"negative", f.actor, -1, ports.ErrInvalidOperationQuery},
		{"large", f.actor, 13, ports.ErrInvalidOperationQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := f.store.ReadDashboardActivity(t.Context(), tc.actor, tc.limit)
			if page != nil || !errors.Is(err, tc.want) {
				t.Fatalf("page %+v err %v", page, err)
			}
		})
	}
	for _, scopes := range [][]string{nil, {"read"}, {"write", "read"}} {
		actor := *f.actor
		actor.Scopes = scopes
		page := dashboardActivityRead(t, f.pool, &actor, 6)
		if page.Items == nil || len(page.Items) != 0 || page.More {
			t.Fatalf("empty page %+v", page)
		}
	}
	execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
	if page, err := f.store.ReadDashboardActivity(t.Context(), f.actor, 6); page != nil || !errors.Is(err, ports.ErrForbidden) {
		t.Fatalf("disabled %+v %v", page, err)
	}
	execIntent(t, f.pool, `UPDATE app_user SET is_active=true,role='admin' WHERE id=$1`, uuid.MustParse(f.actor.ID))
	oldAdmin := *f.actor
	oldAdmin.Role = ports.RoleAdmin
	dashboardActivityRead(t, f.pool, &oldAdmin, 6)
	execIntent(t, f.pool, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.actor.ID))
	if page, err := f.store.ReadDashboardActivity(t.Context(), &oldAdmin, 6); page != nil || !errors.Is(err, ports.ErrForbidden) {
		t.Fatalf("downgrade %+v %v", page, err)
	}
	for _, store := range []*postgres.DashboardActivityStore{nil, postgres.NewDashboardActivityStore(nil)} {
		if page, err := store.ReadDashboardActivity(t.Context(), f.actor, 6); page != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatalf("missing pool %+v %v", page, err)
		}
	}
	closed, err := pgxpool.NewWithConfig(t.Context(), f.pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if page, err := postgres.NewDashboardActivityStore(closed).ReadDashboardActivity(t.Context(), f.actor, 6); page != nil || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("closed pool %+v %v", page, err)
	}
}
func TestDashboardActivityNativeAndStatusesPG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	op := f.add(t, f.actor.ID, "tracking", at)
	for _, tc := range []struct {
		name, update, status string
		progress             *float64
	}{
		{"native cumulative exceeds size", "progress=0.25,downloaded_bytes=500", "tracking", activityFloat(0.25)},
		{"zero", "progress=0", "tracking", activityFloat(0)},
		{"one", "progress=1", "tracking", activityFloat(1)},
		{"missing path", "content_path=NULL", "tracking", nil},
		{"empty path", "content_path=''", "tracking", nil},
		{"error", "error='safe failure'", "tracking", nil},
		{"zero total", "total_bytes=0", "tracking", nil},
		{"negative total", "total_bytes=-1", "tracking", nil},
		{"nan", "progress='NaN'::real", "tracking", nil},
		{"infinity", "progress='Infinity'::real", "tracking", nil},
		{"negative infinity", "progress='-Infinity'::real", "tracking", nil},
		{"negative progress", "progress=-0.1", "tracking", nil},
		{"oversized progress", "progress=1.1", "tracking", nil},
		{"unknown", "state='unknown'", "unknown", nil},
		{"failed", "state='failed'", "failed", nil},
		{"review", "state='completed',organization_state='review_required'", "review_required", nil},
		{"organizing", "state='completed',organization_state='queued'", "organizing", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execIntent(t, f.pool, `UPDATE download_task SET state='tracking',organization_state='pending',content_path='/downloads/item.mkv',error=NULL,progress=0.25,total_bytes=100,downloaded_bytes=500 WHERE operation_id=$1`, op)
			execIntent(t, f.pool, "UPDATE download_task SET "+tc.update+" WHERE operation_id=$1", op)
			before := dashboardActivityGraph(t, f.pool)
			page := dashboardActivityRead(t, f.pool, f.actor, 6)
			if len(page.Items) != 1 || page.More {
				t.Fatalf("page %+v", page)
			}
			item := page.Items[0]
			if item.Status != tc.status || !reflect.DeepEqual(item.Progress, tc.progress) {
				t.Fatalf("item %+v", item)
			}
			if tc.progress == nil && (item.ProgressSource != "" || item.UpdatedAt != nil) {
				t.Fatalf("unsupported telemetry %+v", item)
			}
			assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
		})
	}
	execIntent(t, f.pool, `UPDATE download_task SET state='completed',organization_state='completed' WHERE operation_id=$1`, op)
	if page := dashboardActivityRead(t, f.pool, f.actor, 6); len(page.Items) != 0 || page.More {
		t.Fatalf("terminal %+v", page)
	}
}
func activityFloat(v float64) *float64 { return &v }

type activitySnapshotTrace struct {
	started, release   chan struct{}
	once               sync.Once
	batches, batchSize int
}

func (tr *activitySnapshotTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "-- name: LibraryOverviewSnapshotTime") {
		tr.once.Do(func() {
			close(tr.started)
			select {
			case <-tr.release:
			case <-ctx.Done():
			}
		})
	}
	if strings.Contains(data.SQL, "-- name: ListDashboardActivityProgress") {
		tr.batches++
		if len(data.Args) == 1 {
			if ids, ok := data.Args[0].([]uuid.UUID); ok {
				tr.batchSize = len(ids)
			}
		}
	}
	return ctx
}
func (*activitySnapshotTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestDashboardActivitySameSnapshotPG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	op := f.add(t, f.actor.ID, "tracking", time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC))
	trace := &activitySnapshotTrace{started: make(chan struct{}), release: make(chan struct{})}
	cfg := f.pool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	type result struct {
		page *ports.DashboardActivityPage
		err  error
	}
	results := make(chan result, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		page, err := postgres.NewDashboardActivityStore(pool).ReadDashboardActivity(ctx, f.actor, 1)
		results <- result{page, err}
	}()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(trace.release) }) }
	defer func() { release(); cancel(); <-done; pool.Close() }()
	select {
	case <-trace.started:
	case <-ctx.Done():
		t.Fatal("snapshot gate", ctx.Err())
	}
	// Account and progress change after the permission SELECT established the RR snapshot.
	execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
	execIntent(t, f.pool, `UPDATE download_task SET progress=0.75 WHERE operation_id=$1`, op)
	release()
	var got result
	select {
	case got = <-results:
	case <-ctx.Done():
		t.Fatal("snapshot read", ctx.Err())
	}
	<-done
	if got.err != nil || got.page == nil || len(got.page.Items) != 1 || got.page.Items[0].Progress == nil || *got.page.Items[0].Progress != 0.25 {
		t.Fatalf("mixed snapshot: %+v %v", got.page, got.err)
	}
	if trace.batches != 1 || trace.batchSize != 1 {
		t.Fatalf("unbounded/per-item queries: batches=%d ids=%d", trace.batches, trace.batchSize)
	}
	if page, err := f.store.ReadDashboardActivity(t.Context(), f.actor, 1); page != nil || !errors.Is(err, ports.ErrForbidden) {
		t.Fatalf("next snapshot account: %+v %v", page, err)
	}
}

func TestDashboardActivityReadOnlySnapshotAndFaultPG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	f.add(t, f.actor.ID, "tracking", at)
	execIntent(t, f.pool, `CREATE FUNCTION public.transaction_timestamp() RETURNS timestamptz LANGUAGE plpgsql VOLATILE AS $$ BEGIN
 IF current_setting('transaction_read_only')<>'on' OR current_setting('transaction_isolation')<>'repeatable read' THEN RAISE EXCEPTION 'activity transaction contract'; END IF;
 RETURN '2020-02-03T04:05:06Z'::timestamptz; END $$`)
	cfg := f.pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = "public,pg_catalog"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	page := dashboardActivityRead(t, pool, f.actor, 6)
	want := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
	if !page.ObservedAt.Equal(want) || len(page.Items) != 1 {
		t.Fatalf("snapshot %+v", page)
	}
	execIntent(t, f.pool, `CREATE OR REPLACE FUNCTION public.transaction_timestamp() RETURNS timestamptz LANGUAGE plpgsql VOLATILE AS $$ BEGIN RAISE EXCEPTION 'postgres://private:secret storage fault'; END $$`)
	page, err = postgres.NewDashboardActivityStore(pool).ReadDashboardActivity(t.Context(), f.actor, 6)
	if page != nil || err != ports.ErrUnavailable {
		t.Fatalf("safe storage fault %+v %v", page, err)
	}
}

func TestDashboardActivityAutomaticTVVerifiedPG(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint("large=", large), func(t *testing.T) {
			var f *automaticTVObservationFixture
			if large {
				f = dashboardActivityLargeTVFixture(t)
			} else {
				f = newAutomaticTVObservationFixture(t, true)
			}
			op := f.request.OperationID
			// Authority without a matching committed observation must not expose stale zero.
			page := dashboardActivityRead(t, f.pool, f.actor, 6)
			operations, err := postgres.NewOperationQueryStore(f.pool).ListOperations(t.Context(), f.actor, ports.OperationListRequest{View: "active", Limit: 6})
			if err != nil || operations == nil || len(operations.Items) > 1 {
				t.Fatalf("operation visibility %+v %v", operations, err)
			}
			visibleCount := 1
			if len(page.Items) != visibleCount || page.Items[0].OperationID != op || page.Items[0].Progress != nil {
				t.Fatalf("automatic owner visibility/no evidence %+v; legacy operations %+v", page, operations)
			}
			f.claim(t)
			s := f.load(t)
			remote := automaticTVObservationRemote(s, false)
			verified := s.TotalBytes / 2
			if large {
				verified = s.TotalBytes - 1
			}
			remote.Before[0].VerifiedBytes = verified
			remote.After[0].VerifiedBytes = verified
			receipt, err := f.observer.CommitAutomaticTVObservation(t.Context(), *s, remote)
			if err != nil || receipt.State != "tracking" || receipt.ReviewRequired {
				t.Fatalf("commit %+v %v", receipt, err)
			}
			before := dashboardActivityGraph(t, f.pool)
			page = dashboardActivityRead(t, f.pool, f.actor, 6)
			want := float64(verified) / float64(s.TotalBytes)
			if verified < s.TotalBytes && want >= 1 {
				want = math.Nextafter(1, 0)
			}
			var recorded time.Time
			var stale float32
			if err = f.pool.QueryRow(t.Context(), `SELECT r.created_at,t.progress FROM automatic_tv_observation_result r JOIN download_task t ON t.id=r.task_id WHERE r.job_id=$1`, f.req.Lease.ID).Scan(&recorded, &stale); err != nil {
				t.Fatal(err)
			}
			validRows, queryErr := db.New(f.pool).ListDashboardActivityProgress(t.Context(), []uuid.UUID{op})
			if queryErr != nil || len(validRows) != 1 {
				t.Fatalf("verified batch %+v %v", validRows, queryErr)
			}
			row := validRows[0]
			var tasks []ports.AutomaticTVRemoteTask
			if err = json.Unmarshal([]byte(row.VerifiedTasks), &tasks); err != nil || len(tasks) != 1 {
				t.Fatalf("verified integers %+v %v", tasks, err)
			}
			if stale != 0 || row.Progress != 0 || !row.AutomaticTv || row.ObservationTaskID != row.TaskID || row.ObservationState != "tracking" || row.ReviewRequired || row.TotalBytes != s.TotalBytes || row.DownloadedBytes != verified || tasks[0].TotalBytes != s.TotalBytes || tasks[0].VerifiedBytes != verified || !row.ObservationCreatedAt.Valid || !row.ObservationCreatedAt.Time.Equal(recorded) {
				t.Fatalf("verified batch %+v tasks %+v", row, tasks)
			}
			if len(page.Items) != visibleCount {
				t.Fatalf("visibility changed %+v", page)
			}
			if visibleCount == 1 {
				item := page.Items[0]
				if item.Status != "tracking" || item.Progress == nil || *item.Progress != want || *item.Progress >= 1 || item.ProgressSource != "verified_bytes" || item.UpdatedAt == nil || !item.UpdatedAt.Equal(recorded) {
					t.Fatalf("verified %+v want %.17g stale %v", item, want, stale)
				}
			}
			assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
			f.finishPermit(t)
			// Use real time: the shared claim helper checks statement_timestamp,
			// while the scheduler uses clock_timestamp. Keep their clocks aligned.
			timer := time.NewTimer(2100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
			f.claim(t)
			latest := f.load(t)
			bad := automaticTVObservationRemote(latest, false)
			bad.After[0].VerifiedBytes = latest.TotalBytes + 1
			if large {
				bad.After[0].VerifiedBytes = -1
			}
			receipt, err = f.observer.CommitAutomaticTVObservation(t.Context(), *latest, bad)
			if err != nil || !receipt.ReviewRequired {
				t.Fatalf("bad observation %+v %v", receipt, err)
			}
			latestRows, queryErr := db.New(f.pool).ListDashboardActivityProgress(t.Context(), []uuid.UUID{op})
			if queryErr != nil || len(latestRows) != 1 || latestRows[0].ObservationState != "unknown" || !latestRows[0].ReviewRequired {
				t.Fatalf("latest result selection: %+v %v", latestRows, queryErr)
			}
			before = dashboardActivityGraph(t, f.pool)
			page = dashboardActivityRead(t, f.pool, f.actor, 6)
			if len(page.Items) != visibleCount || (visibleCount == 1 && (page.Items[0].Status != "unknown" || page.Items[0].Progress != nil || page.Items[0].ProgressSource != "" || page.Items[0].UpdatedAt != nil)) {
				t.Fatalf("latest review fallback %+v", page)
			}
			assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
		})
	}
}
func dashboardActivityLargeTVFixture(t *testing.T) *automaticTVObservationFixture {
	t.Helper()
	f := automaticTVSubmissionBase(t, false)
	name := "Trusted.Native.Series.2024.S02E03.mkv"
	total := int64(math.MaxInt64)
	raw := []byte("d4:infod6:lengthi" + strconv.FormatInt(total, 10) + "e4:name" + strconv.Itoa(len(name)) + ":" + name + "12:piece lengthi" + strconv.FormatInt(total, 10) + "e6:pieces20:" + strings.Repeat("0", 20) + "ee")
	proof, err := ports.InspectAutomaticTVMetainfo(torrent.Inspector{}, raw)
	if err != nil {
		t.Fatal("large native metainfo", err)
	}
	round := f.start(t)
	candidate := tvExecutionCandidate(t, f, "activity-large", "1080p", 5)
	hash := proof.InfoHash()
	candidate.InfoHash = &hash
	candidate.SizeBytes = total
	tvExecutionPage(t, f, round, candidate)
	lease := tvExecutionLease(t, f, round)
	decision, err := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), round.ID, lease)
	if err != nil || decision.State != "reserved" {
		t.Fatalf("large authority %+v %v", decision, err)
	}
	job, err := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"automatic_tv_download_submission"}, Column2: "original-sender", Column3: 60000})
	if err != nil {
		t.Fatal(err)
	}
	req := ports.AutomaticTVSendRequest{OperationID: decision.OperationID, Lease: ports.JobLease{ID: job.ID, Owner: "original-sender", Token: job.LeaseToken}}
	original := automaticTVSubmissionFixture{automaticTVFixture: f, store: postgres.NewAutomaticTVSubmissionStore(f.pool, idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32))), request: req, command: ports.PrepareAutomaticTVSubmissionCommand{Request: req, TaskID: uuid.New(), Metainfo: proof}}
	return automaticTVObservationFromSubmission(t, original, true)
}
