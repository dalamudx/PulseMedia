package postgres_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Keep the current reader's schema independent of historical dashboard assertions.
func newOperationActivityFixture(t *testing.T) dashboardActivityFixture {
	t.Helper()
	base := newCreationFixture(t, 93)
	f := dashboardActivityFixture{creationFixture: base, unit: uuid.New(), candidate: uuid.New(), client: uuid.New()}
	media, source := uuid.New(), uuid.New()
	execIntent(t, f.pool, `INSERT INTO media(id,title,media_type) VALUES($1,'Activity movie','movie')`, media)
	execIntent(t, f.pool, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')`, f.unit, media)
	execIntent(t, f.pool, `INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'activity','https://activity.invalid','fixture')`, source)
	execIntent(t, f.pool, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes) VALUES($1,$2,'activity','activity','fixture',100)`, f.candidate, source)
	execIntent(t, f.pool, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted) VALUES($1,'activity','qbittorrent','https://activity.invalid','fixture','fixture')`, f.client)
	return f
}

func activityDetail(t *testing.T, pool *pgxpool.Pool, actor *ports.Actor, id uuid.UUID) *ports.OperationSnapshot {
	t.Helper()
	got, err := postgres.NewActivityOperationQueryStore(pool).ReadOperation(t.Context(), actor, id)
	if err != nil || got == nil || got.Telemetry == nil {
		t.Fatalf("detail %+v %v", got, err)
	}
	return got
}
func activityPage(t *testing.T, pool *pgxpool.Pool, actor *ports.Actor, request ports.OperationListRequest) *ports.OperationPage {
	t.Helper()
	got, err := postgres.NewActivityOperationQueryStore(pool).ListOperations(t.Context(), actor, request)
	if err != nil || got == nil || got.Items == nil {
		t.Fatalf("page %+v %v", got, err)
	}
	return got
}
func activityGraph(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	out := timelineGraph(t, pool)
	var rates string
	if err := pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY task_id),'[]'::jsonb)::text FROM download_rate_observation r`).Scan(&rates); err != nil {
		t.Fatal(err)
	}
	out["download_rate_observation"] = rates
	return out
}

func TestOperationActivityAuthorizationPG(t *testing.T) {
	f := newOperationActivityFixture(t)
	op := f.add(t, f.actor.ID, "tracking", time.Now())
	hidden := f.add(t, uuid.NewString(), "tracking", time.Now())
	store := postgres.NewActivityOperationQueryStore(f.pool)
	for _, tc := range []struct {
		name  string
		actor *ports.Actor
	}{
		{"nil", nil},
		{"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: f.actor.ID, Role: ports.RoleAdmin}},
		{"bad ID", &ports.Actor{Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleMember}},
		{"nil ID", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.Nil.String(), Role: ports.RoleMember}},
		{"bad role", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: "guest"}},
		{"empty scope", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleMember, Scopes: []string{}}},
		{"write scope", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleMember, Scopes: []string{"write"}}},
		{"claimed admin", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleAdmin}},
		{"missing user", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := store.ReadOperation(t.Context(), tc.actor, op); got != nil || !errors.Is(err, ports.ErrForbidden) {
				t.Fatalf("detail %+v %v", got, err)
			}
			if got, err := store.ListOperations(t.Context(), tc.actor, ports.OperationListRequest{View: "all", Limit: 1}); got != nil || !errors.Is(err, ports.ErrForbidden) {
				t.Fatalf("list %+v %v", got, err)
			}
		})
	}
	for _, scopes := range [][]string{nil, {"read"}, {"write", "read"}} {
		actor := *f.actor
		actor.Scopes = scopes
		activityDetail(t, f.pool, &actor, op)
		if page := activityPage(t, f.pool, &actor, ports.OperationListRequest{View: "all", Limit: 50}); len(page.Items) != 1 || page.Items[0].OperationID != op {
			t.Fatalf("own list %+v", page)
		}
	}
	for _, tc := range []struct {
		id   uuid.UUID
		want error
	}{{hidden, ports.ErrForbidden}, {uuid.New(), ports.ErrNotFound}, {uuid.Nil, ports.ErrInvalidOperationQuery}} {
		if got, err := store.ReadOperation(t.Context(), f.actor, tc.id); got != nil || !errors.Is(err, tc.want) {
			t.Fatalf("visibility %+v %v", got, err)
		}
	}
	admin := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	execIntent(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'activity-admin','fixture','admin')`, uuid.MustParse(admin.ID))
	activityDetail(t, f.pool, admin, hidden)
	if page := activityPage(t, f.pool, admin, ports.OperationListRequest{View: "all", Limit: 50}); len(page.Items) != 2 {
		t.Fatalf("admin %+v", page)
	}
	for _, update := range []string{`UPDATE app_user SET is_active=false WHERE id=$1`, `UPDATE app_user SET is_active=true,role='admin' WHERE id=$1`} {
		execIntent(t, f.pool, update, uuid.MustParse(f.actor.ID))
		if got, err := store.ReadOperation(t.Context(), f.actor, op); got != nil || err != ports.ErrForbidden {
			t.Fatalf("current account %+v %v", got, err)
		}
		if got, err := store.ListOperations(t.Context(), f.actor, ports.OperationListRequest{View: "all", Limit: 1}); got != nil || err != ports.ErrForbidden {
			t.Fatalf("current list %+v %v", got, err)
		}
	}
}

func TestOperationActivityPagingFiltersAndCursorPG(t *testing.T) {
	f := newOperationActivityFixture(t)
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	var want []uuid.UUID
	for range 7 {
		want = append(want, f.add(t, f.actor.ID, "tracking", at))
	}
	sort.Slice(want, func(i, j int) bool { return want[i].String() > want[j].String() })
	older := f.add(t, f.actor.ID, "unknown", at.Add(-time.Hour))
	want = append(want, older)
	completed := f.add(t, f.actor.ID, "completed", at.Add(-2*time.Hour))
	execIntent(t, f.pool, `UPDATE download_task SET organization_state='completed' WHERE operation_id=$1`, completed)
	want = append(want, completed)
	// A shared operation still occupies one page slot.
	execIntent(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,created_at) VALUES($1,$2,$3,$4,'user',$5,$6)`, uuid.New(), want[0], f.unit, f.movie, uuid.NewString(), at)
	before := activityGraph(t, f.pool)
	for _, limit := range []int{1, 3, 50} {
		var got []uuid.UUID
		cursor := ""
		for {
			page := activityPage(t, f.pool, f.actor, ports.OperationListRequest{View: "all", Limit: limit, Cursor: cursor})
			for _, item := range page.Items {
				got = append(got, item.OperationID)
				detail := activityDetail(t, f.pool, f.actor, item.OperationID)
				if !reflect.DeepEqual(item.OperationSnapshot, *detail) {
					t.Fatal("detail/list mismatch")
				}
			}
			if page.NextCursor == "" {
				break
			}
			if page.NextCursor == cursor || len(got) >= len(want) {
				t.Fatal("cursor did not advance")
			}
			cursor = page.NextCursor
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("order %v want %v", got, want)
		}
	}
	if p := activityPage(t, f.pool, f.actor, ports.OperationListRequest{View: "active", Limit: 50}); len(p.Items) != 8 {
		t.Fatalf("active %+v", p)
	}
	if p := activityPage(t, f.pool, f.actor, ports.OperationListRequest{View: "review", Limit: 50}); len(p.Items) != 1 || p.Items[0].OperationID != older {
		t.Fatalf("review %+v", p)
	}
	cursor := activityPage(t, f.pool, f.actor, ports.OperationListRequest{View: "all", Limit: 1}).NextCursor
	raw, _ := base64.RawURLEncoding.DecodeString(cursor)
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	bad := []string{"bad", strings.Repeat("a", 513), cursor + "\n", base64.RawURLEncoding.EncodeToString([]byte(string(raw) + "{}")), base64.RawURLEncoding.EncodeToString([]byte("{}"))}
	for _, field := range []string{"a", "r", "v", "i", "t", "extra"} {
		altered := make(map[string]any)
		for k, v := range fields {
			altered[k] = v
		}
		altered[field] = "bad"
		if field == "i" {
			altered[field] = uuid.Nil.String()
		}
		if field == "t" {
			altered[field] = "0001-01-01T00:00:00Z"
		}
		encoded, _ := json.Marshal(altered)
		bad = append(bad, base64.RawURLEncoding.EncodeToString(encoded))
	}
	store := postgres.NewActivityOperationQueryStore(f.pool)
	for _, c := range bad {
		if got, err := store.ListOperations(t.Context(), f.actor, ports.OperationListRequest{View: "all", Limit: 1, Cursor: c}); got != nil || err != ports.ErrInvalidOperationQuery {
			t.Fatalf("bad cursor %+v %v", got, err)
		}
	}
	for _, req := range []ports.OperationListRequest{{View: "all", Limit: 0}, {View: "all", Limit: -1}, {View: "all", Limit: 51}, {View: "bad", Limit: 1}, {View: "active", Limit: 1, Cursor: cursor}} {
		if got, err := store.ListOperations(t.Context(), f.actor, req); got != nil || err != ports.ErrInvalidOperationQuery {
			t.Fatalf("bad request %+v %v", got, err)
		}
	}
	promoted := *f.actor
	promoted.Role = ports.RoleAdmin
	execIntent(t, f.pool, `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(f.actor.ID))
	if got, err := store.ListOperations(t.Context(), &promoted, ports.OperationListRequest{View: "all", Limit: 1, Cursor: cursor}); got != nil || err != ports.ErrInvalidOperationQuery {
		t.Fatalf("role cursor %+v %v", got, err)
	}
	// Catalog mutations above are intentional; reads leave the business graph unchanged.
	assertEpisodeRecheckSnapshot(t, before, activityGraph(t, f.pool))
}

func TestOperationActivitySystemOnlyQueuedPG(t *testing.T) {
	f := newTVExecutionFixture(t, 93)
	round := f.start(t)
	tvExecutionPage(t, f, round, tvExecutionCandidate(t, f, "activity-auto", "1080p", 5))
	decision, err := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), round.ID, tvExecutionLease(t, f, round))
	if err != nil || decision.State != "reserved" {
		t.Fatalf("finalize %+v %v", decision, err)
	}
	op := decision.OperationID
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND actor_type='user'`, op); n != 0 {
		t.Fatal("forged user demand")
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task WHERE operation_id=$1`, op); n != 0 {
		t.Fatal("must precede task")
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_execution_authority WHERE operation_id=$1 AND owner_id=$2 AND unit_id=$3 AND root_id=$4`, op, uuid.MustParse(f.actor.ID), f.unit, f.tv); n != 1 {
		t.Fatal("missing real owner authority")
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='automatic_tv_download_submission' AND state='queued' AND dedupe_key=$1`, op.String()); n != 1 {
		t.Fatal("missing real queued job")
	}
	outsider := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	admin := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	for _, a := range []*ports.Actor{outsider, admin} {
		execIntent(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,$2,'fixture',$3)`, uuid.MustParse(a.ID), a.ID, a.Role)
	}
	before := activityGraph(t, f.pool)
	for _, a := range []*ports.Actor{f.actor, admin} {
		got := activityDetail(t, f.pool, a, op)
		if got.Status != "queued" || got.DownloadStatus != nil || got.Progress != nil || got.Telemetry.Progress != nil || got.Telemetry.RateStatus != "unknown" {
			t.Fatalf("queued %+v", got)
		}
		p := activityPage(t, f.pool, a, ports.OperationListRequest{View: "active", Limit: 1})
		if len(p.Items) != 1 || p.Items[0].OperationID != op || p.NextCursor != "" {
			t.Fatalf("owner list %+v", p)
		}
	}
	p := activityPage(t, f.pool, outsider, ports.OperationListRequest{View: "all", Limit: 1})
	if len(p.Items) != 0 {
		t.Fatal("private operation listed")
	}
	if got, err := postgres.NewActivityOperationQueryStore(f.pool).ReadOperation(t.Context(), outsider, op); got != nil || err != ports.ErrForbidden {
		t.Fatalf("private %+v %v", got, err)
	}
	if p := activityPage(t, f.pool, f.actor, ports.OperationListRequest{View: "review", Limit: 1}); len(p.Items) != 0 {
		t.Fatal("queued is review")
	}
	assertEpisodeRecheckSnapshot(t, before, activityGraph(t, f.pool))
}

func TestOperationActivityTelemetryAndPlanPG(t *testing.T) {
	f := newOperationActivityFixture(t)
	op := f.add(t, f.actor.ID, "tracking", time.Now())
	task, stamp := rateTask(t, f.pool, op)
	rate := int64(math.MaxInt64)
	if err := rateRecord(t, f.pool, task, stamp, &rate, "ordinary", true); err != nil {
		t.Fatal(err)
	}
	before := activityGraph(t, f.pool)
	got := activityDetail(t, f.pool, f.actor, op)
	if got.Progress == nil || *got.Progress != 0.25 || got.Telemetry.Progress == nil || *got.Telemetry.Progress != 0.25 || got.Telemetry.ProgressSource != "native_percent" || got.Telemetry.DownloadRateBytesPerSecond == nil || *got.Telemetry.DownloadRateBytesPerSecond != "9223372036854775807" || got.Telemetry.RateStatus != "current" {
		t.Fatalf("telemetry %+v", got)
	}
	p := activityPage(t, f.pool, f.actor, ports.OperationListRequest{View: "all", Limit: 1})
	if !reflect.DeepEqual(p.Items[0].OperationSnapshot, *got) {
		t.Fatal("enrichment differs")
	}
	encoded, _ := json.Marshal(p)
	for _, private := range []string{"/downloads", "https://", "cipher", "password", "content_path", "save_path"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private output %s", private)
		}
	}
	assertEpisodeRecheckSnapshot(t, before, activityGraph(t, f.pool))
	execIntent(t, f.pool, `UPDATE download_rate_observation SET observed_at=clock_timestamp()-interval '46 seconds' WHERE task_id=$1`, task.ID)
	if got := activityDetail(t, f.pool, f.actor, op); got.Telemetry.RateStatus != "stale" || got.Telemetry.DownloadRateBytesPerSecond == nil {
		t.Fatal("lost stale sample")
	}
	execIntent(t, f.pool, `UPDATE download_task SET content_path=NULL,total_bytes=0,progress=1 WHERE operation_id=$1`, op)
	if got := activityDetail(t, f.pool, f.actor, op); got.Progress != nil || got.Telemetry.Progress != nil {
		t.Fatal("invented progress")
	}
	plan := timelinePlan(t, f, op)
	execIntent(t, f.pool, `UPDATE download_task SET transfer_plan_id=$2 WHERE operation_id=$1`, op, plan)
	retired := timelinePlan(t, f, op)
	execIntent(t, f.pool, `UPDATE transfer_plan SET state='failed' WHERE id=$1`, retired)
	if got := activityDetail(t, f.pool, f.actor, op); got.PlanID == nil || *got.PlanID != plan || *got.PlanStatus != "created" {
		t.Fatalf("current plan %+v", got)
	}
	for _, tc := range []struct{ organization, status string }{{"completed", "completed"}, {"queued", "organizing"}, {"review_required", "review_required"}, {"pending", "waiting_organization"}} {
		execIntent(t, f.pool, `UPDATE download_task SET state='completed',organization_state=$2 WHERE operation_id=$1`, op, tc.organization)
		if got := activityDetail(t, f.pool, f.actor, op); got.Status != tc.status || got.Progress != nil {
			t.Fatalf("status %+v", got)
		}
	}
	// Real automatic observations retain float64 verified progress through this reader.
	auto := dashboardActivityLargeTVFixture(t)
	if version, err := postgres.Migrate(t.Context(), auto.pool.Config().ConnString(), repositoryMigrationsThrough(t, 93)); err != nil || version != 93 {
		t.Fatalf("schema93 %d %v", version, err)
	}
	auto.claim(t)
	snapshot := auto.load(t)
	remote := automaticTVObservationRemote(snapshot, false)
	remote.Before[0].VerifiedBytes = snapshot.TotalBytes - 1
	remote.After[0].VerifiedBytes = snapshot.TotalBytes - 1
	if receipt, err := auto.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, remote); err != nil || receipt.State != "tracking" {
		t.Fatalf("native observation %+v %v", receipt, err)
	}
	verified := activityDetail(t, auto.pool, auto.actor, auto.request.OperationID)
	if verified.Progress == nil || *verified.Progress != math.Nextafter32(1, 0) || verified.Telemetry.Progress == nil || *verified.Telemetry.Progress != math.Nextafter(1, 0) || verified.Telemetry.ProgressSource != "verified_bytes" {
		t.Fatalf("verified %+v", verified)
	}
}

type operationActivitySnapshotTrace struct {
	mu               sync.Mutex
	queries          []string
	started, release chan struct{}
	once             sync.Once
}

func (tr *operationActivitySnapshotTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	tr.queries = append(tr.queries, data.SQL)
	tr.mu.Unlock()
	if strings.Contains(data.SQL, "-- name: ListActivityOperationSummaries") && tr.started != nil {
		tr.once.Do(func() {
			close(tr.started)
			select {
			case <-tr.release:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}
func (*operationActivitySnapshotTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}
func TestOperationActivitySnapshotAndStorageFaultPG(t *testing.T) {
	f := newOperationActivityFixture(t)
	op := f.add(t, f.actor.ID, "tracking", time.Now())
	trace := &operationActivitySnapshotTrace{started: make(chan struct{}), release: make(chan struct{})}
	cfg := f.pool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	type result struct {
		page *ports.OperationPage
		err  error
	}
	results := make(chan result, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p, e := postgres.NewActivityOperationQueryStore(pool).ListOperations(ctx, f.actor, ports.OperationListRequest{View: "all", Limit: 50})
		results <- result{p, e}
	}()
	var once sync.Once
	release := func() { once.Do(func() { close(trace.release) }) }
	defer func() { release(); cancel(); <-done; pool.Close() }()
	select {
	case <-trace.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
	execIntent(t, f.pool, `UPDATE download_task SET state='unknown',progress=0.75 WHERE operation_id=$1`, op)
	release()
	var got result
	select {
	case got = <-results:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	<-done
	if got.err != nil || got.page == nil || len(got.page.Items) != 1 || got.page.Items[0].Status != "tracking" || got.page.Items[0].Progress == nil || *got.page.Items[0].Progress != 0.25 {
		t.Fatalf("mixed snapshot %+v %v", got.page, got.err)
	}
	trace.mu.Lock()
	queries := append([]string(nil), trace.queries...)
	trace.mu.Unlock()
	for _, name := range []string{"begin isolation level repeatable read read only", "-- name: GetUserByID", "-- name: ListActivityOperationSummaries", "-- name: ListDashboardActivityProgress", "-- name: ListOperationDownloadRates", "commit"} {
		count := 0
		for _, q := range queries {
			if strings.Contains(q, name) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("query %q count %d: %v", name, count, queries)
		}
	}
	store := postgres.NewActivityOperationQueryStore(pool)
	if got, err := store.ReadOperation(t.Context(), f.actor, op); got != nil || err != ports.ErrForbidden {
		t.Fatalf("next snapshot %+v %v", got, err)
	}
	execIntent(t, f.pool, `UPDATE app_user SET is_active=true WHERE id=$1`, uuid.MustParse(f.actor.ID))
	before := activityGraph(t, f.pool)
	activityDetail(t, pool, f.actor, op)
	activityPage(t, pool, f.actor, ports.OperationListRequest{View: "all", Limit: 1})
	assertEpisodeRecheckSnapshot(t, before, activityGraph(t, f.pool))
	closed, err := pgxpool.NewWithConfig(t.Context(), f.pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	for _, broken := range []*postgres.OperationQueryStore{postgres.NewActivityOperationQueryStore(nil), postgres.NewActivityOperationQueryStore(closed)} {
		if got, err := broken.ReadOperation(t.Context(), f.actor, op); got != nil || err != ports.ErrUnavailable {
			t.Fatalf("storage detail %+v %v", got, err)
		}
		if got, err := broken.ListOperations(t.Context(), f.actor, ports.OperationListRequest{View: "all", Limit: 1}); got != nil || err != ports.ErrUnavailable {
			t.Fatalf("storage list %+v %v", got, err)
		}
	}
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	if got, err := store.ReadOperation(cancelled, f.actor, op); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel detail %+v %v", got, err)
	}
	if got, err := store.ListOperations(cancelled, f.actor, ports.OperationListRequest{View: "all", Limit: 1}); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel list %+v %v", got, err)
	}
	execIntent(t, f.pool, `ALTER TABLE download_rate_observation RENAME TO unavailable_rate_observation`)
	if got, err := store.ReadOperation(t.Context(), f.actor, op); got != nil || err != ports.ErrUnavailable {
		t.Fatalf("query failure detail %+v %v", got, err)
	}
	if got, err := store.ListOperations(t.Context(), f.actor, ports.OperationListRequest{View: "all", Limit: 1}); got != nil || err != ports.ErrUnavailable {
		t.Fatalf("query failure list %+v %v", got, err)
	}
	t.Log("ONE_READ_ONLY_RR_BOUNDED_BATCH_AND_NO_BUSINESS_WRITES_VERIFIED")
}
