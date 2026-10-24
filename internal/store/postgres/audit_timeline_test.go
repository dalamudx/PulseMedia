package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/auditlog"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/audit"
	"github.com/google/uuid"
)

func auditFixture(t *testing.T) mediaRequestFixture {
	t.Helper()
	f := newMediaRequestFixture(t)
	f.history = repositoryMigrationsThrough(t, 118)
	if version, err := postgres.Migrate(t.Context(), f.dsn, f.history); err != nil || version != 118 {
		t.Fatal("audit migration", version, err)
	}
	return f
}

func auditSeed(t *testing.T, f mediaRequestFixture, at time.Time, actorType, actorID, action, targetType, targetID string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	f.exec(t, `INSERT INTO audit_log(id,actor_type,actor_id,action,target_type,target_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,'{"cookie":"owned-audit-secret"}',$7)`, id, actorType, actorID, action, targetType, targetID, at)
	return id
}

func TestAuditTimelinePaginationAndReadOnlyPG(t *testing.T) {
	f := auditFixture(t)
	at := time.Date(2030, 1, 2, 3, 4, 5, 123000, time.UTC)
	for range 205 {
		auditSeed(t, f, at, "user", f.owner.ID, "timeline.seed", "media", "fixture")
	}
	svc := audit.NewTimelineService(postgres.NewAuditTimelineStore(f.pool))
	before := mediaInboxSnapshot(t, f)
	request := ports.AuditTimelineRequest{Filter: auditlog.Filter{Action: "timeline.seed"}}
	page, err := svc.List(t.Context(), f.admin, request)
	if err != nil || len(page.Items) != 50 || page.NextCursor == "" {
		t.Fatal("first page", page, err)
	}
	if !reflect.DeepEqual(before, mediaInboxSnapshot(t, f)) {
		t.Fatal("first page wrote business tables")
	}
	seen := map[uuid.UUID]bool{}
	for _, row := range page.Items {
		seen[row.ID] = true
	}
	cursorID := page.Items[len(page.Items)-1].ID
	newer := auditSeed(t, f, at.Add(time.Second), "user", f.owner.ID, "timeline.seed", "media", "newer")
	// A retention removal of the already-returned cursor must not break continuation.
	f.exec(t, `DELETE FROM audit_log WHERE id=$1`, cursorID)
	before = mediaInboxSnapshot(t, f)
	for pages := 0; page.NextCursor != ""; pages++ {
		if pages > 5 {
			t.Fatal("cursor did not terminate")
		}
		request.Cursor = page.NextCursor
		page, err = svc.List(t.Context(), f.admin, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Items {
			if seen[row.ID] || row.ID == newer {
				t.Fatal("duplicate or new head in continuation")
			}
			seen[row.ID] = true
		}
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "owned-audit-secret") || strings.Contains(string(raw), "payload") {
			t.Fatal("historical payload exposed")
		}
	}
	if len(seen) != 205 || !reflect.DeepEqual(before, mediaInboxSnapshot(t, f)) {
		t.Fatal("omission or read mutation", len(seen))
	}
}

func TestAuditTimelineExactFiltersPG(t *testing.T) {
	f := auditFixture(t)
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	ids := []uuid.UUID{
		auditSeed(t, f, at, "user", f.owner.ID, "movie.create", "media", "a"),
		auditSeed(t, f, at.Add(time.Second), "user", f.other.ID, "literal_%'", "media", "ab"),
		auditSeed(t, f, at.Add(2*time.Second), "system", "system", "movie.create", "job", "a"),
		auditSeed(t, f, at.Add(3*time.Second), "service", "worker", "movie.create", "media", "a"),
	}
	cases := []struct {
		filter auditlog.Filter
		want   []uuid.UUID
	}{
		{auditlog.Filter{ActorType: "system"}, []uuid.UUID{ids[2]}},
		{auditlog.Filter{ActorID: f.other.ID}, []uuid.UUID{ids[1]}},
		{auditlog.Filter{Action: "literal_%'"}, []uuid.UUID{ids[1]}},
		{auditlog.Filter{TargetType: "job"}, []uuid.UUID{ids[2]}},
		{auditlog.Filter{TargetID: "a", TargetType: "media"}, []uuid.UUID{ids[3], ids[0]}},
		{auditlog.Filter{From: at.Add(time.Second), Until: at.Add(3 * time.Second)}, []uuid.UUID{ids[2], ids[1]}},
		{auditlog.Filter{Action: "literal"}, []uuid.UUID{}},
	}
	before := mediaInboxSnapshot(t, f)
	svc := audit.NewTimelineService(postgres.NewAuditTimelineStore(f.pool))
	for _, tc := range cases {
		page, err := svc.List(t.Context(), f.admin, ports.AuditTimelineRequest{Filter: tc.filter, Limit: 200})
		if err != nil || page.Items == nil || page.NextCursor != "" {
			t.Fatal("filtered read", page, err)
		}
		got := make([]uuid.UUID, 0, len(page.Items))
		for _, row := range page.Items {
			got = append(got, row.ID)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatal("filter mismatch", tc.filter, got, tc.want)
		}
	}
	if !reflect.DeepEqual(before, mediaInboxSnapshot(t, f)) {
		t.Fatal("filters wrote data")
	}
}

func TestAuditTimelineCurrentPermissionPG(t *testing.T) {
	f := auditFixture(t)
	reader := postgres.NewAuditTimelineStore(f.pool)
	token := *f.admin
	token.Scopes = []string{"read"}
	if rows, err := reader.ReadAuditTimeline(t.Context(), &token, auditlog.Filter{}, nil, 51); err != nil || rows == nil {
		t.Fatal("current admin read scope", rows, err)
	}
	forged := *f.owner
	forged.Role = ports.RoleAdmin
	for _, a := range []*ports.Actor{nil, f.owner, &forged, {Type: ports.ActorTypeSystem, ID: f.admin.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: f.admin.ID, Role: ports.RoleAdmin, Scopes: []string{}}, {Type: ports.ActorTypeUser, ID: f.admin.ID, Role: ports.RoleAdmin, Scopes: []string{"read", "write"}}} {
		before := mediaInboxSnapshot(t, f)
		rows, err := reader.ReadAuditTimeline(t.Context(), a, auditlog.Filter{}, nil, 51)
		if rows != nil || (!errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized)) {
			t.Fatal("identity accepted", rows, err)
		}
		if !reflect.DeepEqual(before, mediaInboxSnapshot(t, f)) {
			t.Fatal("denied read wrote data")
		}
	}
	for _, statement := range []string{`UPDATE app_user SET role='member' WHERE id=$1`, `UPDATE app_user SET role='admin',is_active=false WHERE id=$1`, `DELETE FROM app_user WHERE id=$1`} {
		f.exec(t, statement, uuid.MustParse(f.admin.ID))
		before := mediaInboxSnapshot(t, f)
		rows, err := reader.ReadAuditTimeline(t.Context(), f.admin, auditlog.Filter{}, nil, 51)
		if rows != nil || !errors.Is(err, ports.ErrForbidden) {
			t.Fatal("stale administrator accepted", rows, err)
		}
		if !reflect.DeepEqual(before, mediaInboxSnapshot(t, f)) {
			t.Fatal("revoked read wrote data")
		}
	}
}

func auditWaitForAccountLock(t *testing.T, f mediaRequestFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%LockSubscriptionListAccount%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("account read did not wait")
		case <-ticker.C:
		}
	}
}

func TestAuditTimelineWaitedRevocationAndCancellationPG(t *testing.T) {
	f := auditFixture(t)
	reader := postgres.NewAuditTimelineStore(f.pool)
	for _, cancelRead := range []bool{false, true} {
		f.exec(t, `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(f.admin.ID))
		tx, err := f.pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.admin.ID)); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		result := make(chan error, 1)
		done := make(chan struct{})
		go func() {
			_, err := reader.ReadAuditTimeline(ctx, f.admin, auditlog.Filter{}, nil, 51)
			result <- err
			close(done)
		}()
		t.Cleanup(func() {
			cancel()
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_ = tx.Rollback(cleanup)
			select {
			case <-done:
			case <-cleanup.Done():
				t.Error("audit query did not stop")
			}
		})
		auditWaitForAccountLock(t, f)
		expected := ports.ErrForbidden
		if cancelRead {
			cancel()
			expected = context.Canceled
		} else if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if !errors.Is(err, expected) {
				t.Fatal("wait result", err, expected)
			}
		case <-ctx.Done():
			if !cancelRead {
				t.Fatal("read did not complete")
			}
			if err := <-result; !errors.Is(err, expected) {
				t.Fatal(err)
			}
		}
		if cancelRead {
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		cancel()
	}
}

func TestAuditTimelineStorageFailuresAndDatabaseReadOnlyPG(t *testing.T) {
	f := auditFixture(t)
	reader := postgres.NewAuditTimelineStore(f.pool)
	auditSeed(t, f, time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC), "system", "system", "seed", "system", "system")
	for _, limit := range []int{0, -1, 202} {
		if rows, err := reader.ReadAuditTimeline(t.Context(), f.admin, auditlog.Filter{}, nil, limit); rows != nil || !errors.Is(err, ports.ErrRejected) {
			t.Fatal("invalid limit", rows, err)
		}
	}
	f.exec(t, `ALTER TABLE audit_log RENAME TO audit_log_private`)
	before := mediaInboxSnapshot(t, f)
	if rows, err := reader.ReadAuditTimeline(t.Context(), f.admin, auditlog.Filter{}, nil, 51); rows != nil || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("storage error became empty success", rows, err)
	}
	f.exec(t, `CREATE FUNCTION audit_timeline_write_fault(value timestamptz) RETURNS timestamptz LANGUAGE plpgsql AS $$ BEGIN INSERT INTO audit_log_private(id,actor_type,actor_id,action,target_type,target_id,payload,created_at) VALUES(gen_random_uuid(),'system','system','forbidden.read','system','system','{}',clock_timestamp()); RETURN value; END $$`)
	f.exec(t, `CREATE VIEW audit_log AS SELECT id,actor_type,actor_id,action,target_type,target_id,payload,audit_timeline_write_fault(created_at) AS created_at FROM audit_log_private`)
	if rows, err := reader.ReadAuditTimeline(t.Context(), f.admin, auditlog.Filter{}, nil, 51); rows != nil || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("database read-only gate bypassed", rows, err)
	}
	if !reflect.DeepEqual(before, mediaInboxSnapshot(t, f)) {
		t.Fatal("read executed a database write")
	}
	f.exec(t, `DROP VIEW audit_log`)
	f.exec(t, `DROP FUNCTION audit_timeline_write_fault(timestamptz)`)
	f.exec(t, `ALTER TABLE audit_log_private RENAME TO audit_log`)
	if rows, err := reader.ReadAuditTimeline(t.Context(), f.admin, auditlog.Filter{}, nil, 51); err != nil || len(rows) != 1 {
		t.Fatal("recovery read", rows, err)
	}
}

func TestAuditTimelineBusinessRollbackAndSchemaHistoryPG(t *testing.T) {
	f := auditFixture(t)
	svc := audit.NewTimelineService(postgres.NewAuditTimelineStore(f.pool))
	original := f.submit(t, f.owner, "audit-visible")
	request := ports.AuditTimelineRequest{Filter: auditlog.Filter{ActorID: f.owner.ID, TargetID: original.Request.ID.String()}}
	page, err := svc.List(t.Context(), f.admin, request)
	if err != nil || len(page.Items) != 1 || page.Items[0].Action == "" || page.Items[0].ActorType != "user" {
		t.Fatal("committed business audit missing", page, err)
	}
	_ = f.submit(t, f.owner, "audit-visible")
	again, err := svc.List(t.Context(), f.admin, request)
	if err != nil || !reflect.DeepEqual(page, again) {
		t.Fatal("original key duplicated audit", again, err)
	}
	f.exec(t, `CREATE FUNCTION audit_tail_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned tail failure'; END $$`)
	f.exec(t, `CREATE TRIGGER audit_tail_fault BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION audit_tail_fault()`)
	before := mediaInboxSnapshot(t, f)
	if result, err := f.store.SubmitMediaRequest(t.Context(), f.other, mediaRequestIntent(), "audit-rollback"); result != nil || err == nil {
		t.Fatal("tail did not fail", result, err)
	}
	if !reflect.DeepEqual(before, mediaInboxSnapshot(t, f)) {
		t.Fatal("business rollback leaked audit or success rows")
	}
	f.exec(t, `DROP TRIGGER audit_tail_fault ON idempotency_record`)
	f.exec(t, `DROP FUNCTION audit_tail_fault()`)
	before = mediaInboxSnapshot(t, f)
	delete(before, "goose_db_version")
	if version, err := postgres.Rollback(t.Context(), f.dsn, f.history); err != nil || version != 117 {
		t.Fatal("index rollback", version, err)
	}
	if version, err := postgres.Migrate(t.Context(), f.dsn, f.history); err != nil || version != 118 {
		t.Fatal("index reinstall", version, err)
	}
	after := mediaInboxSnapshot(t, f)
	delete(after, "goose_db_version")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("index migration changed historical audit or business records")
	}
	if n := f.count(t, `SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND indexname IN ('audit_log_timeline_idx','audit_log_actor_timeline_idx','audit_log_action_timeline_idx','audit_log_target_timeline_idx')`); n != 4 {
		t.Fatal("missing timeline indexes", n)
	}
	// The old standalone writer and all existing transactional writers share this ledger.
	if _, err := db.New(f.pool).ListAuditLogs(t.Context(), db.ListAuditLogsParams{Limit: 10}); err != nil {
		t.Fatal("legacy reader changed", err)
	}
}
