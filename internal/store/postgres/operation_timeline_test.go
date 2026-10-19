package postgres_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

func timelineRead(t *testing.T, pool *pgxpool.Pool, actor *ports.Actor, op uuid.UUID, request ports.OperationTimelineRequest) *ports.OperationTimelinePage {
	t.Helper()
	page, err := postgres.NewOperationTimelineStore(pool).ReadOperationTimeline(t.Context(), actor, op, request)
	if err != nil || page == nil {
		t.Fatalf("timeline: %+v %v", page, err)
	}
	return page
}

func timelinePlan(t *testing.T, f dashboardActivityFixture, op uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	execIntent(t, f.pool, `INSERT INTO transfer_plan(id,operation_id,root_id,state,strategy) VALUES($1,$2,$3,'created','hardlink')`, id, op, f.movie)
	return id
}

func timelineStep(t *testing.T, f dashboardActivityFixture, plan uuid.UUID, order int, target, source any) uuid.UUID {
	t.Helper()
	id := uuid.New()
	execIntent(t, f.pool, `INSERT INTO transfer_step(id,plan_id,unit_id,source_path,target_path,action,state,step_order,source_size,source_mtime,target_rel,source_rel,updated_at) VALUES($1,$2,$3,'/private/source/passkey','/private/target/token','copy','pending',$4,123,456,$5,$6,'2020-01-02T03:04:05Z')`, id, plan, f.unit, order, target, source)
	return id
}

func timelineGraph(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	out := dashboardActivityGraph(t, pool)
	var value string
	if err := pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY id),'[]'::jsonb)::text FROM transfer_step s`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	out["transfer_step"] = value
	return out
}

func TestOperationTimelinePaginationPlanAndStatusPG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	op := f.add(t, f.actor.ID, "tracking", time.Now())
	empty := timelineRead(t, f.pool, f.actor, op, ports.OperationTimelineRequest{})
	if empty.Items == nil || len(empty.Items) != 0 || empty.PlanID != nil || empty.PlanStatus != nil || empty.NextCursor != "" || empty.ObservedAt.IsZero() {
		t.Fatalf("no plan %+v", empty)
	}
	plan := timelinePlan(t, f, op)
	execIntent(t, f.pool, `UPDATE download_task SET transfer_plan_id=$2 WHERE operation_id=$1`, op, plan)
	retired := timelinePlan(t, f, op)
	timelineStep(t, f, retired, -99, "retired.mkv", nil)
	type ordered struct {
		id    uuid.UUID
		order int32
	}
	want := make([]ordered, 0, 107)
	for i := 106; i >= 0; i-- {
		want = append(want, ordered{timelineStep(t, f, plan, i/3, "TV/episode.mkv", nil), int32(i / 3)})
	}
	sort.Slice(want, func(i, j int) bool {
		if want[i].order != want[j].order {
			return want[i].order < want[j].order
		}
		return want[i].id.String() < want[j].id.String()
	})
	before := timelineGraph(t, f.pool)
	for _, limit := range []int{0, 1, 50, 100} {
		cursor := ""
		index := 0
		for {
			page := timelineRead(t, f.pool, f.actor, op, ports.OperationTimelineRequest{Limit: limit, Cursor: cursor})
			n := limit
			if n == 0 {
				n = 50
			}
			remaining := len(want) - index
			if remaining < n {
				n = remaining
			}
			if page.PlanID == nil || *page.PlanID != plan || page.PlanStatus == nil || *page.PlanStatus != "created" || len(page.Items) != n {
				t.Fatalf("page %+v", page)
			}
			for _, item := range page.Items {
				if item.StepID != want[index].id || item.Order != want[index].order || item.Action != "copy" || item.State != "pending" || item.FileName != "episode.mkv" || item.FinishedAt != nil || !item.UpdatedAt.Equal(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)) {
					t.Fatalf("item %d %+v", index, item)
				}
				index++
			}
			if page.NextCursor == "" {
				if index != len(want) {
					t.Fatalf("lost steps %d", index)
				}
				break
			}
			if index >= len(want) || page.NextCursor == cursor {
				t.Fatal("pagination did not advance")
			}
			cursor = page.NextCursor
		}
	}
	assertEpisodeRecheckSnapshot(t, before, timelineGraph(t, f.pool))
	first := timelineRead(t, f.pool, f.actor, op, ports.OperationTimelineRequest{Limit: 1})
	store := postgres.NewOperationTimelineStore(f.pool)
	other := f.add(t, f.actor.ID, "tracking", time.Now())
	if p, e := store.ReadOperationTimeline(t.Context(), f.actor, other, ports.OperationTimelineRequest{Cursor: first.NextCursor}); p != nil || !errors.Is(e, ports.ErrInvalidOperationQuery) {
		t.Fatalf("operation fence %+v %v", p, e)
	}
	execIntent(t, f.pool, `UPDATE download_task SET transfer_plan_id=$2 WHERE operation_id=$1`, op, retired)
	if p, e := store.ReadOperationTimeline(t.Context(), f.actor, op, ports.OperationTimelineRequest{Cursor: first.NextCursor}); p != nil || !errors.Is(e, ports.ErrInvalidOperationQuery) {
		t.Fatalf("plan fence %+v %v", p, e)
	}
	for _, state := range []string{"created", "approved", "executing", "review_required", "completed", "failed"} {
		execIntent(t, f.pool, `UPDATE transfer_plan SET state=$2 WHERE id=$1`, retired, state)
		for _, taskState := range []string{"tracking", "completed", "failed", "canceled", "unknown"} {
			execIntent(t, f.pool, `UPDATE download_task SET state=$2 WHERE operation_id=$1`, op, taskState)
			page := timelineRead(t, f.pool, f.actor, op, ports.OperationTimelineRequest{})
			if *page.PlanStatus != state || len(page.Items) != 1 || page.Items[0].State != "pending" {
				t.Fatalf("status projection %+v", page)
			}
		}
	}
}

func TestOperationTimelineAuthorizationPG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	op := f.add(t, f.actor.ID, "tracking", time.Now())
	hidden := f.add(t, uuid.NewString(), "tracking", time.Now())
	store := postgres.NewOperationTimelineStore(f.pool)
	for _, tc := range []struct {
		name    string
		actor   *ports.Actor
		op      uuid.UUID
		request ports.OperationTimelineRequest
		want    error
	}{
		{"nil", nil, op, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: f.actor.ID, Role: ports.RoleAdmin}, op, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"bad id", &ports.Actor{Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleMember}, op, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"nil id", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.Nil.String(), Role: ports.RoleMember}, op, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"role", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: "guest"}, op, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"empty scopes", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleMember, Scopes: []string{}}, op, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"write scopes", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleMember, Scopes: []string{"write"}}, op, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"forged admin", &ports.Actor{Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleAdmin}, op, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"missing user", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}, op, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"private", f.actor, hidden, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"absent", f.actor, uuid.New(), ports.OperationTimelineRequest{}, ports.ErrNotFound},
		{"nil operation", f.actor, uuid.Nil, ports.OperationTimelineRequest{}, ports.ErrInvalidOperationQuery},
		{"negative limit", f.actor, op, ports.OperationTimelineRequest{Limit: -1}, ports.ErrInvalidOperationQuery},
		{"large limit", f.actor, op, ports.OperationTimelineRequest{Limit: 101}, ports.ErrInvalidOperationQuery},
		{"malformed cursor", f.actor, op, ports.OperationTimelineRequest{Cursor: "bogus"}, ports.ErrInvalidOperationQuery},
		{"oversized cursor", f.actor, op, ports.OperationTimelineRequest{Cursor: strings.Repeat("a", 1025)}, ports.ErrInvalidOperationQuery},
		{"empty cursor object", f.actor, op, ports.OperationTimelineRequest{Cursor: base64.RawURLEncoding.EncodeToString([]byte("{}"))}, ports.ErrInvalidOperationQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, e := store.ReadOperationTimeline(t.Context(), tc.actor, tc.op, tc.request)
			if p != nil || !errors.Is(e, tc.want) {
				t.Fatalf("page %+v err %v", p, e)
			}
		})
	}
	for _, scopes := range [][]string{nil, {"read"}, {"write", "read"}} {
		a := *f.actor
		a.Scopes = scopes
		timelineRead(t, f.pool, &a, op, ports.OperationTimelineRequest{})
	}
	admin := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	execIntent(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'timeline-admin','fixture','admin')`, uuid.MustParse(admin.ID))
	timelineRead(t, f.pool, admin, hidden, ports.OperationTimelineRequest{})
	plan := timelinePlan(t, f, op)
	timelineStep(t, f, plan, 0, "one.mkv", nil)
	timelineStep(t, f, plan, 1, "two.mkv", nil)
	cursor := timelineRead(t, f.pool, f.actor, op, ports.OperationTimelineRequest{Limit: 1}).NextCursor
	if p, e := store.ReadOperationTimeline(t.Context(), admin, op, ports.OperationTimelineRequest{Cursor: cursor}); p != nil || !errors.Is(e, ports.ErrInvalidOperationQuery) {
		t.Fatalf("actor fence %+v %v", p, e)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(cursor)
	// Valid JSON with an extra field or trailing value is not a cursor issued by this reader.
	for _, bad := range []string{string(raw[:len(raw)-1]) + `,"extra":true}`, string(raw) + `{}`} {
		if p, e := store.ReadOperationTimeline(t.Context(), f.actor, op, ports.OperationTimelineRequest{Cursor: base64.RawURLEncoding.EncodeToString([]byte(bad))}); p != nil || !errors.Is(e, ports.ErrInvalidOperationQuery) {
			t.Fatalf("noncanonical cursor %+v %v", p, e)
		}
	}
	execIntent(t, f.pool, `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(f.actor.ID))
	promoted := *f.actor
	promoted.Role = ports.RoleAdmin
	if p, e := store.ReadOperationTimeline(t.Context(), &promoted, op, ports.OperationTimelineRequest{Cursor: cursor}); p != nil || !errors.Is(e, ports.ErrInvalidOperationQuery) {
		t.Fatalf("role fence %+v %v", p, e)
	}
	if p, e := store.ReadOperationTimeline(t.Context(), f.actor, op, ports.OperationTimelineRequest{}); p != nil || !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("current role %+v %v", p, e)
	}
	execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(promoted.ID))
	if p, e := store.ReadOperationTimeline(t.Context(), &promoted, op, ports.OperationTimelineRequest{}); p != nil || !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("disabled %+v %v", p, e)
	}
	for _, s := range []*postgres.OperationTimelineStore{nil, postgres.NewOperationTimelineStore(nil)} {
		if p, e := s.ReadOperationTimeline(t.Context(), admin, op, ports.OperationTimelineRequest{}); p != nil || e != ports.ErrUnavailable {
			t.Fatalf("nil pool %+v %v", p, e)
		}
	}
	closed, e := pgxpool.NewWithConfig(t.Context(), f.pool.Config())
	if e != nil {
		t.Fatal(e)
	}
	closed.Close()
	if p, e := postgres.NewOperationTimelineStore(closed).ReadOperationTimeline(t.Context(), admin, op, ports.OperationTimelineRequest{}); p != nil || e != ports.ErrUnavailable {
		t.Fatalf("closed pool %+v %v", p, e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if p, e := store.ReadOperationTimeline(ctx, admin, op, ports.OperationTimelineRequest{}); p != nil || !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel %+v %v", p, e)
	}
}

func TestOperationTimelineSafeLabelsAndStepFactsPG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	op := f.add(t, f.actor.ID, "completed", time.Now())
	plan := timelinePlan(t, f, op)
	cases := []struct {
		target, source any
		want           string
	}{
		{"Season 1/节目.mkv", nil, "节目.mkv"}, {nil, "incoming/source.mkv", "source.mkv"}, {"", "incoming/fallback.mkv", "fallback.mkv"},
		{nil, nil, ""}, {"/private/secret", "safe/fallback.mkv", ""}, {"../secret", nil, ""}, {"a/../secret", nil, ""}, {"a//secret", nil, ""}, {"a/./secret", nil, ""}, {"C:\\secret", nil, ""}, {"https://user:pass@host/secret", nil, ""}, {"a\\secret", nil, ""}, {"a/%2e%2e/secret", nil, ""}, {"a/name?passkey=secret", nil, ""}, {"a/name#secret", nil, ""}, {"a/line\nsecret", nil, ""}, {"a/\u202esecret", nil, ""}, {".", nil, ""}, {"", "../bad", ""},
	}
	states := []string{"pending", "running", "completed", "failed", "skipped"}
	for i, tc := range cases {
		id := timelineStep(t, f, plan, i, tc.target, tc.source)
		// Freeze real catalog versions before recording a completed movie step.
		execIntent(t, f.pool, `UPDATE transfer_step s SET target_evidence=jsonb_build_object('unit_id',u.id,'media_id',m.id,'unit_key',u.unit_key,'unit_version',u.metadata_version,'media_version',m.metadata_version,'parser_version','episode.v1') FROM media_unit u JOIN media m ON m.id=u.media_id WHERE s.id=$1 AND u.id=s.unit_id`, id)
		execIntent(t, f.pool, `UPDATE transfer_step SET state=$2,action=$3,finished_at=CASE WHEN $2='completed' THEN '2020-01-03T04:05:06Z'::timestamptz ELSE NULL END,error='https://user:secret@private/?passkey=secret' WHERE id=$1`, id, states[i%len(states)], []string{"copy", "hardlink", "reflink", "move"}[i%4])
	}
	before := timelineGraph(t, f.pool)
	page := timelineRead(t, f.pool, f.actor, op, ports.OperationTimelineRequest{})
	if len(page.Items) != len(cases) {
		t.Fatalf("items %d", len(page.Items))
	}
	for i, item := range page.Items {
		if item.FileName != cases[i].want || item.State != states[i%5] || item.Action != []string{"copy", "hardlink", "reflink", "move"}[i%4] || (item.FinishedAt != nil) != (item.State == "completed") {
			t.Fatalf("fact %d %+v", i, item)
		}
		if item.FinishedAt != nil && !item.FinishedAt.Equal(time.Date(2020, 1, 3, 4, 5, 6, 0, time.UTC)) {
			t.Fatalf("finished %+v", item)
		}
	}
	encoded, _ := json.Marshal(page)
	for _, secret := range []string{"/private", "passkey", "https://", "secret", "source_path", "target_path", "root_path"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("private data %s: %s", secret, encoded)
		}
	}
	assertEpisodeRecheckSnapshot(t, before, timelineGraph(t, f.pool))
}

func TestOperationTimelineSystemOnlyAutomaticTVPG(t *testing.T) {
	f := newTVExecutionFixture(t)
	round := f.start(t)
	candidate := tvExecutionCandidate(t, f, "timeline-auto", "1080p", 5)
	tvExecutionPage(t, f, round, candidate)
	lease := tvExecutionLease(t, f, round)
	decision, err := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), round.ID, lease)
	if err != nil || decision.State != "reserved" {
		t.Fatalf("real authority %+v %v", decision, err)
	}
	var users, systems int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE actor_type='user'),count(*) FILTER(WHERE actor_type='system') FROM download_requirement WHERE operation_id=$1`, decision.OperationID).Scan(&users, &systems); err != nil || users != 0 || systems < 1 {
		t.Fatalf("not a system-only fixture users=%d systems=%d %v", users, systems, err)
	}
	before := timelineGraph(t, f.pool)
	page := timelineRead(t, f.pool, f.actor, decision.OperationID, ports.OperationTimelineRequest{})
	if page.Items == nil || len(page.Items) != 0 || page.PlanID != nil {
		t.Fatalf("auto timeline %+v", page)
	}
	outsider := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	// Catalog users are mutable; the immutable demand and authority stay untouched.
	execIntent(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'timeline-outsider','fixture','member')`, uuid.MustParse(outsider.ID))
	if p, e := postgres.NewOperationTimelineStore(f.pool).ReadOperationTimeline(t.Context(), outsider, decision.OperationID, ports.OperationTimelineRequest{}); p != nil || !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("auto private %+v %v", p, e)
	}
	execIntent(t, f.pool, `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(outsider.ID))
	outsider.Role = ports.RoleAdmin
	timelineRead(t, f.pool, outsider, decision.OperationID, ports.OperationTimelineRequest{})
	execIntent(t, f.pool, `UPDATE subscription SET state='paused' WHERE id=$1`, f.sub)
	timelineRead(t, f.pool, f.actor, decision.OperationID, ports.OperationTimelineRequest{})
	// Compare all business state except the deliberately changed user/subscription catalogs.
	after := timelineGraph(t, f.pool)
	delete(before, "subscription")
	delete(after, "subscription")
	delete(before, "app_user")
	delete(after, "app_user")
	assertEpisodeRecheckSnapshot(t, before, after)
}

type timelineSnapshotTrace struct {
	started, release chan struct{}
	once             sync.Once
}

func (tr *timelineSnapshotTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "-- name: OperationTimelineSnapshotTime") {
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
func (*timelineSnapshotTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestOperationTimelineSnapshotAndStorageFaultPG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	op := f.add(t, f.actor.ID, "tracking", time.Now())
	plan := timelinePlan(t, f, op)
	id := timelineStep(t, f, plan, 0, "one.mkv", nil)
	trace := &timelineSnapshotTrace{started: make(chan struct{}), release: make(chan struct{})}
	cfg := f.pool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	type result struct {
		page *ports.OperationTimelinePage
		err  error
	}
	results := make(chan result, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p, e := postgres.NewOperationTimelineStore(pool).ReadOperationTimeline(ctx, f.actor, op, ports.OperationTimelineRequest{})
		results <- result{p, e}
	}()
	var once sync.Once
	release := func() { once.Do(func() { close(trace.release) }) }
	defer func() { release(); cancel(); <-done; pool.Close() }()
	select {
	case <-trace.started:
	case <-ctx.Done():
		t.Fatal("snapshot gate", ctx.Err())
	}
	execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
	execIntent(t, f.pool, `UPDATE transfer_step SET state='running' WHERE id=$1`, id)
	execIntent(t, f.pool, `UPDATE transfer_plan SET state='approved' WHERE id=$1`, plan)
	release()
	var got result
	select {
	case got = <-results:
	case <-ctx.Done():
		t.Fatal("snapshot read", ctx.Err())
	}
	<-done
	if got.err != nil || got.page == nil || len(got.page.Items) != 1 || got.page.Items[0].State != "pending" || *got.page.PlanStatus != "created" {
		t.Fatalf("mixed snapshot %+v %v", got.page, got.err)
	}
	if p, e := postgres.NewOperationTimelineStore(f.pool).ReadOperationTimeline(t.Context(), f.actor, op, ports.OperationTimelineRequest{}); p != nil || !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("next snapshot account %+v %v", p, e)
	}
	execIntent(t, f.pool, `UPDATE app_user SET is_active=true WHERE id=$1`, uuid.MustParse(f.actor.ID))
	execIntent(t, f.pool, `CREATE FUNCTION public.transaction_timestamp() RETURNS timestamptz LANGUAGE plpgsql VOLATILE AS $$ BEGIN
 IF current_setting('transaction_read_only')<>'on' OR current_setting('transaction_isolation')<>'repeatable read' THEN RAISE EXCEPTION 'timeline transaction contract'; END IF;
 RETURN '2020-02-03T04:05:06Z'::timestamptz; END $$`)
	readonly := f.pool.Config()
	readonly.ConnConfig.RuntimeParams["search_path"] = "public,pg_catalog"
	readonlyPool, err := pgxpool.NewWithConfig(t.Context(), readonly)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonlyPool.Close)
	before := timelineGraph(t, f.pool)
	page := timelineRead(t, readonlyPool, f.actor, op, ports.OperationTimelineRequest{})
	if !page.ObservedAt.Equal(time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)) {
		t.Fatalf("DB clock %+v", page)
	}
	if !reflect.DeepEqual(before, timelineGraph(t, f.pool)) {
		t.Fatal("read changed business graph")
	}
	execIntent(t, f.pool, `CREATE OR REPLACE FUNCTION public.transaction_timestamp() RETURNS timestamptz LANGUAGE plpgsql VOLATILE AS $$ BEGIN RAISE EXCEPTION 'postgres://user:secret@private'; END $$`)
	if p, e := postgres.NewOperationTimelineStore(readonlyPool).ReadOperationTimeline(t.Context(), f.actor, op, ports.OperationTimelineRequest{}); p != nil || e != ports.ErrUnavailable {
		t.Fatalf("storage fault %+v %v", p, e)
	}
	assertEpisodeRecheckSnapshot(t, before, timelineGraph(t, f.pool))
	t.Log("READ_ONLY_RR_AND_BUSINESS_GRAPH_VERIFIED")
}
