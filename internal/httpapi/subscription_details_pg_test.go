package httpapi_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type subscriptionDetailsTrace struct {
	mu    sync.Mutex
	sql   []string
	after func()
}

func (t *subscriptionDetailsTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	t.mu.Lock()
	t.sql = append(t.sql, d.SQL)
	t.mu.Unlock()
	return context.WithValue(ctx, subscriptionListTraceKey{}, d.SQL)
}
func (t *subscriptionDetailsTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(subscriptionListTraceKey{}).(string)
	t.mu.Lock()
	var after func()
	if strings.HasPrefix(sql, "-- name: GetSubscriptionByID") {
		after = t.after
		t.after = nil
	}
	t.mu.Unlock()
	if after != nil {
		after()
	}
}

func TestSubscriptionDetailsCurrentReadOnlySnapshotPG(t *testing.T) {
	a, adminSession, memberSession, rootID, unit, _, _, _ := setupOperationHTTP(t)
	ctx := t.Context()
	pool, q := a.DBPool(), a.Queries()
	admin, err := q.GetSessionByTokenHash(ctx, crypto.HashToken(adminSession))
	if err != nil {
		t.Fatal(err)
	}
	member, err := q.GetSessionByTokenHash(ctx, crypto.HashToken(memberSession))
	if err != nil {
		t.Fatal(err)
	}
	var mediaID uuid.UUID
	if err = pool.QueryRow(ctx, "SELECT media_id FROM media_unit WHERE id=$1", unit).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	sub, err := q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: uuid.New(), UserID: member.UserID, MediaID: mediaID, RootID: rootID, QualityPolicyID: "default", QualityPolicyRevision: 1, IndexerIds: []byte("[]"), ScopeType: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.UpsertWantedItem(ctx, db.UpsertWantedItemParams{ID: uuid.New(), SubscriptionID: sub.ID, UnitID: unit, IntentVersion: 1, NextEvaluateAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	media, err := q.GetMediaByID(ctx, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	root, err := q.GetLibraryRootByID(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	trace := &subscriptionDetailsTrace{}
	cfg := pool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.ConnConfig.Tracer = trace
	readPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer readPool.Close()
	reader := postgres.NewSubscriptionListStore(readPool)
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.UserID.String(), Role: ports.RoleAdmin}
	trace.after = func() {
		if _, err := pool.Exec(ctx, "UPDATE media SET title='changed after snapshot' WHERE id=$1", mediaID); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "UPDATE library_root SET name='changed after snapshot' WHERE id=$1", rootID); err != nil {
			t.Error(err)
		}
	}
	out, err := reader.ReadSubscriptionDetails(ctx, actor, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != media.Title || out.RootName != root.Name || out.RootPath == nil || len(out.WantedItems) != 1 {
		t.Fatalf("mixed snapshot: %+v", out)
	}
	trace.mu.Lock()
	queries := append([]string(nil), trace.sql...)
	trace.mu.Unlock()
	foundReadOnly := false
	for _, sql := range queries {
		foundReadOnly = foundReadOnly || strings.Contains(sql, "SET TRANSACTION READ ONLY")
		upper := strings.ToUpper(strings.TrimSpace(sql))
		if strings.HasPrefix(upper, "INSERT ") || strings.HasPrefix(upper, "UPDATE ") || strings.HasPrefix(upper, "DELETE ") {
			t.Fatal("read wrote data", sql)
		}
	}
	if !foundReadOnly {
		t.Fatal("missing read-only transaction")
	}
	if _, err = reader.ReadSubscriptionDetails(ctx, actor, uuid.New()); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("missing", err)
	}
	memberActor := &ports.Actor{Type: ports.ActorTypeUser, ID: member.UserID.String(), Role: ports.RoleMember}
	out, err = reader.ReadSubscriptionDetails(ctx, memberActor, sub.ID)
	if err != nil || out.RootPath != nil {
		t.Fatalf("member path: %+v %v", out, err)
	}
	denied := *memberActor
	denied.Scopes = []string{"admin"}
	if _, err = reader.ReadSubscriptionDetails(ctx, &denied, sub.ID); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("scope escalation", err)
	}
	foreign := *memberActor
	foreign.ID = admin.UserID.String()
	if _, err = reader.ReadSubscriptionDetails(ctx, &foreign, sub.ID); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("current role", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = reader.ReadSubscriptionDetails(cancelled, actor, sub.ID); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("cancelled storage", err)
	}
	if _, err = pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", admin.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ReadSubscriptionDetails(ctx, actor, sub.ID); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("current revocation", err)
	}
}
