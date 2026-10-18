package settings_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/command"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSettingFinalWriteFailureRollsBackAndRetries(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			pool, s, q, a, _ := setupSettingsPoolTest(t)
			ctx := context.Background()
			req := settings.UpdateSettingRequest{Value: map[string]any{"flag": "original"}}
			_, e := pool.Exec(ctx, `CREATE FUNCTION owned_setting_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned setting failure'; END $$`)
			if e != nil {
				t.Fatal(e)
			}
			_, e = pool.Exec(ctx, `CREATE TRIGGER owned_setting_failure BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION owned_setting_fail()`)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.UpdateSetting(ctx, a, "fault.atomic", req, "retry-original-failure"); e == nil {
				t.Fatal("fault accepted")
			}
			for _, sql := range []string{`SELECT count(*) FROM setting`, `SELECT count(*) FROM audit_log`, `SELECT count(*) FROM outbox_event`, `SELECT count(*) FROM idempotency_record`} {
				var n int
				if e = pool.QueryRow(ctx, sql).Scan(&n); e != nil || n != 0 {
					t.Fatalf("rollback failed: %s count%d err%v", sql, n, e)
				}
			}
			if _, e = pool.Exec(ctx, `DROP TRIGGER owned_setting_failure ON `+table); e != nil {
				t.Fatal(e)
			}
			r, e := s.UpdateSetting(ctx, a, "fault.atomic", req, "retry-original-failure")
			if e != nil || r.FromCache || r.Data.Version != 1 {
				t.Fatal("original-key recovery failed", e)
			}
			replay, e := s.UpdateSetting(ctx, a, "fault.atomic", req, "retry-original-failure")
			if e != nil || !replay.FromCache {
				t.Fatal("replay failed", e)
			}
			logs, e := q.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10})
			if e != nil || len(logs) != 1 {
				t.Fatal("audits duplicated", e)
			}
			var events, receipts int
			if e = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM outbox_event),(SELECT count(*) FROM idempotency_record)`).Scan(&events, &receipts); e != nil || events != 1 || receipts != 1 {
				t.Fatalf("event/receipt duplicated %d/%d %v", events, receipts, e)
			}
		})
	}
}
func TestSettingFirstCreationSerializesDistinctAdmins(t *testing.T) {
	pool, s, q, a, _ := setupSettingsPoolTest(t)
	ctx := context.Background()
	id := uuid.New()
	_, e := q.CreateUser(ctx, db.CreateUserParams{ID: id, Username: "second-admin", PasswordHash: "unusable-fixture-hash", Role: ports.RoleAdmin, IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	other := &ports.Actor{Type: ports.ActorTypeUser, ID: id.String(), Role: ports.RoleAdmin}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, actor := range []*ports.Actor{a, other} {
		wg.Add(1)
		go func(i int, actor *ports.Actor) {
			defer wg.Done()
			<-start
			_, err := s.UpdateSetting(ctx, actor, "first.concurrent", settings.UpdateSettingRequest{Value: map[string]any{"winner": i}}, actor.ID)
			errs <- err
		}(i, actor)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for e := range errs {
		if e == nil {
			successes++
		} else if errors.Is(e, ports.ErrSettingVersionConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("create results %d/%d", successes, conflicts)
	}
	var version, events, receipts int
	if e = pool.QueryRow(ctx, `SELECT (SELECT version FROM setting WHERE key='first.concurrent'),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM idempotency_record)`).Scan(&version, &events, &receipts); e != nil || version != 1 || events != 1 || receipts != 1 {
		t.Fatalf("first creation committed more than once %d/%d/%d %v", version, events, receipts, e)
	}
}
func TestSettingCancelledWaitDoesNotLeaveReceipt(t *testing.T) {
	pool, s, _, a, _ := setupSettingsPoolTest(t)
	ctx := context.Background()
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Hold the same resource identity before any setting row exists.
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(json_build_array('setting'::text,'cancel.resource'::text)::text,0))`)
	if e != nil {
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	req := settings.UpdateSettingRequest{Value: map[string]any{"flag": true}}
	if _, e = s.UpdateSetting(deadline, a, "cancel.resource", req, "cancelled-original-key"); e == nil {
		t.Fatal("blocked request accepted")
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	r, e := s.UpdateSetting(ctx, a, "cancel.resource", req, "cancelled-original-key")
	if e != nil || r.FromCache || r.Data.Version != 1 {
		t.Fatal("cancelled request left state or lock", e)
	}
	var receipts int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_record`).Scan(&receipts); e != nil || receipts != 1 {
		t.Fatal("partial receipt left", e)
	}
}
func TestSettingLegacyReceiptRequiresSameObjectWithoutResigning(t *testing.T) {
	pool, s, q, a, _ := setupSettingsPoolTest(t)
	ctx := context.Background()
	req := settings.UpdateSettingRequest{Value: map[string]any{"flag": "legacy"}}
	row, e := q.SetSetting(ctx, db.SetSettingParams{Key: "legacy.original", Value: []byte(`{"flag":"legacy"}`)})
	if e != nil {
		t.Fatal(e)
	}
	body, e := json.Marshal(settings.SettingItem{Key: row.Key, Value: req.Value, Version: row.Version, UpdatedAt: row.UpdatedAt.Time})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(req)
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256(raw)
	fp := hex.EncodeToString(h[:])
	original, e := q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: uuid.New(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: "legacy-receipt", RequestHash: fp, ResponseCode: 200, ResponseBody: body, ResourceID: pgtype.Text{String: row.Key, Valid: true}})
	if e != nil {
		t.Fatal(e)
	}
	result, e := s.UpdateSetting(ctx, a, row.Key, req, "legacy-receipt")
	if e != nil || !result.FromCache || result.Data.Version != 1 {
		t.Fatal("verifiable legacy recovery failed", e)
	}
	if _, e = s.UpdateSetting(ctx, a, "legacy.other", req, "legacy-receipt"); !errors.Is(e, command.ErrIdempotencyConflict) {
		t.Fatal("legacy receipt expanded to other object", e)
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: "legacy-receipt"})
	if e != nil || old.RequestHash != fp || string(old.ResponseBody) != string(original.ResponseBody) {
		t.Fatal("legacy receipt resigned", e)
	}
	var events int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_event`).Scan(&events); e != nil || events != 0 {
		t.Fatal("legacy replay published new event", e)
	}
}

func TestSettingReceiptReadRequiresLiveOwnerAndMasksSecret(t *testing.T) {
	pool, s, q, a, _ := setupSettingsPoolTest(t)
	ctx := context.Background()
	req := settings.UpdateSettingRequest{Value: map[string]any{"api_key": "synthetic-private-receipt"}}
	if _, e := s.UpdateSetting(ctx, a, "tmdb.api_key", req, "own-metadata-key"); e != nil {
		t.Fatal(e)
	}
	result, e := s.ReadSettingReceipt(ctx, a, "tmdb.api_key", "own-metadata-key")
	if e != nil || !result.FromCache {
		t.Fatal("receipt unavailable", e)
	}
	body, e := json.Marshal(result.Data)
	if e != nil || strings.Contains(string(body), "synthetic-private-receipt") || !strings.Contains(string(body), `"configured":true`) {
		t.Fatal("receipt secret not masked", e)
	}
	if _, e = s.ReadSettingReceipt(ctx, a, "other-setting", "own-metadata-key"); !errors.Is(e, command.ErrIdempotencyConflict) {
		t.Fatal("receipt object boundary bypass", e)
	}
	if _, e = s.ReadSettingReceipt(ctx, a, "tmdb.api_key", "not-committed"); !errors.Is(e, ports.ErrNotFound) {
		t.Fatal("unknown receipt claimed success", e)
	}
	otherID := uuid.New()
	_, e = q.CreateUser(ctx, db.CreateUserParams{ID: otherID, Username: "receipt-other-admin", PasswordHash: "unusable-hash", Role: ports.RoleAdmin, IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	other := &ports.Actor{Type: ports.ActorTypeUser, ID: otherID.String(), Role: ports.RoleAdmin}
	if _, e = s.ReadSettingReceipt(ctx, other, "tmdb.api_key", "own-metadata-key"); !errors.Is(e, ports.ErrNotFound) {
		t.Fatal("another admin read original user receipt", e)
	}
	api := *a
	api.Scopes = []string{"read"}
	if _, e = s.ReadSettingReceipt(ctx, &api, "tmdb.api_key", "own-metadata-key"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("API credential recovered session receipt", e)
	}
	if _, e = pool.Exec(ctx, `UPDATE app_user SET role='member' WHERE id=$1`, a.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadSettingReceipt(ctx, a, "tmdb.api_key", "own-metadata-key"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("stale actor read original metadata", e)
	}
	var version, audits, events, receipts int
	e = pool.QueryRow(ctx, `SELECT (SELECT version FROM setting WHERE key='tmdb.api_key'),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM idempotency_record)`).Scan(&version, &audits, &events, &receipts)
	if e != nil || version != 1 || audits != 1 || events != 1 || receipts != 1 {
		t.Fatal("read/rejection modified durable facts", e)
	}
}
