package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

func TestM2CommandsCurrentUserBeforeReceipt(t *testing.T) {
	a, _, token, root, unit, _, router, _ := setupOperationHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	q, pool := a.Queries(), a.DBPool()
	u, e := q.GetSearchUnit(ctx, unit)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(httpapi.CreateSubscriptionRequest{MediaId: &u.MediaID, RootId: &root})
	req := httptest.NewRequest("POST", "/api/v1/subscriptions", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	var detail httpapi.SubscriptionDetails
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &detail) != nil {
		t.Fatal("subscription fixture", response.Code)
	}
	owner, e := q.GetUserByUsername(ctx, "admin")
	if e != nil {
		t.Fatal(e)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: owner.ID.String(), Role: owner.Role}
	if actor.Role != ports.RoleAdmin {
		t.Fatal("fixture requires original administrator")
	}
	ids := idgen.New()
	pol := policy.NewService(postgres.NewPolicyStore(pool, ids))
	control := subscription.NewControlService(postgres.NewSubscriptionControlStore(pool, ids))
	document, e := pol.Read(ctx, "default", 0)
	if e != nil {
		t.Fatal(e)
	}
	rules := document.Rules
	rules.RejectGroups = []string{"ASAP"}
	pc := ports.PolicyCommand{Kind: "publish", ID: "default", ExpectedRevision: document.Revision, Rules: &rules}
	pr, e := pol.Command(ctx, actor, pc, "m2-policy-once")
	if e != nil {
		t.Fatal(e)
	}
	updated, e := q.GetSubscriptionByID(ctx, detail.Id)
	if e != nil {
		t.Fatal(e)
	}
	sc := ports.SubscriptionControlCommand{Action: "state", State: "paused", ExpectedVersion: updated.IntentVersion}
	sr, e := control.Apply(ctx, actor, detail.Id, sc, "m2-control-once")
	if e != nil {
		t.Fatal(e)
	}
	snapshot := func() [5]int64 {
		var v [5]int64
		if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM policy_revision),(SELECT sum(intent_version) FROM subscription),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM idempotency_record)").Scan(&v[0], &v[1], &v[2], &v[3], &v[4]); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := snapshot()
	denied := func(who *ports.Actor) {
		t.Helper()
		for _, key := range []string{"m2-policy-once", "m2-policy-new"} {
			if _, err := pol.Command(ctx, who, pc, key); !errors.Is(err, ports.ErrForbidden) {
				t.Fatalf("policy accepted stale actor: %v", err)
			}
		}
		for _, key := range []string{"m2-control-once", "m2-control-new"} {
			if _, err := control.Apply(ctx, who, detail.Id, sc, key); !errors.Is(err, ports.ErrForbidden) {
				t.Fatalf("scope accepted stale actor: %v", err)
			}
		}
		if snapshot() != before {
			t.Fatal("rejected command changed business/audit/event/receipt")
		}
	}
	for _, query := range []string{"UPDATE app_user SET is_active=false WHERE id=$1", "UPDATE app_user SET role='member' WHERE id=$1"} {
		if _, e = pool.Exec(ctx, query, owner.ID); e != nil {
			t.Fatal(e)
		}
		denied(actor)
		if _, e = pool.Exec(ctx, "UPDATE app_user SET is_active=true,role='admin' WHERE id=$1", owner.ID); e != nil {
			t.Fatal(e)
		}
	}
	for _, who := range []*ports.Actor{{Type: ports.ActorTypeSystem, ID: actor.ID, Role: actor.Role}, {Type: ports.ActorTypeUser, ID: actor.ID, Role: actor.Role, Scopes: []string{"read"}}, {Type: ports.ActorTypeUser, ID: uuid.New().String(), Role: actor.Role}} {
		denied(who)
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", owner.ID); e != nil {
		t.Fatal(e)
	}
	results := make(chan error, 2)
	go func() { _, err := pol.Command(ctx, actor, pc, "m2-policy-once"); results <- err }()
	go func() { _, err := control.Apply(ctx, actor, detail.Id, sc, "m2-control-once"); results <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting int
		if e = pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%app_user%'").Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("commands never reached user lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		select {
		case err := <-results:
			if !errors.Is(err, ports.ErrForbidden) {
				t.Fatal("post-lock stale permission", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if snapshot() != before {
		t.Fatal("lock-wait denial changed persistent state")
	}
	if _, e = pool.Exec(ctx, "UPDATE app_user SET is_active=true WHERE id=$1", owner.ID); e != nil {
		t.Fatal(e)
	}
	recovered, e := pol.Command(ctx, actor, pc, "m2-policy-once")
	if e != nil || *recovered != *pr {
		t.Fatal("policy receipt recovery", e)
	}
	restored, e := control.Apply(ctx, actor, detail.Id, sc, "m2-control-once")
	if e != nil || *restored != *sr {
		t.Fatal("scope receipt recovery", e)
	}
	if snapshot() != before {
		t.Fatal("successful replay duplicated writes")
	}
}
