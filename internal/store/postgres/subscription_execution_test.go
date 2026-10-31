package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestSubscriptionExecutionOwnerReceiptPrivacyPG(t *testing.T) {
	f := newCreationFixture(t, 131)
	ctx := context.Background()
	created := f.create(t, creationInput(7501, "movie"), creationMovie(7501), "diagnostic-create")
	id := created.Subscription.ID
	repo := postgres.NewSubscriptionExecutionStore(f.pool)
	svc := subscription.NewExecutionService(repo)
	other := *f.actor
	other.ID = uuid.NewString()
	if _, e := db.New(f.pool).CreateUser(ctx, db.CreateUserParams{ID: uuid.MustParse(other.ID), Username: "other", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.Read(ctx, &other, id); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("cross owner", e)
	}
	indexer, query, round, candidate := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	wanted := created.Wanted[0].ID
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'private','https://tracker.invalid/private',decode('01','hex'))`, []any{indexer}},
		{`INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,error,finished_at) VALUES($1,$2,'search','private query',1,'failed','https://secret.invalid/passkey',clock_timestamp())`, []any{query, indexer}},
		{`INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes) VALUES($1,$2,'https://secret.invalid/title','private-guid',decode('01','hex'),1)`, []any{candidate, indexer}},
		{`INSERT INTO search_round(id,subscription_id,wanted_item_id,snapshot_intent_version,snapshot_policy_revision,state,deadline,abort_reason,candidate_ids) VALUES($1,$2,$3,1,1,'aborted',clock_timestamp()+interval '1 minute','https://secret.invalid/abort',jsonb_build_array($4::text))`, []any{round, id, wanted, candidate}},
		{`INSERT INTO search_round_endpoint(round_id,indexer_id,query_id,state) VALUES($1,$2,$3,'failed')`, []any{round, indexer, query}},
		{`INSERT INTO release_block(subscription_id,candidate_id,blocked) VALUES($1,$2,true)`, []any{id, candidate}},
	}
	for _, s := range statements {
		if _, e := f.pool.Exec(ctx, s.sql, s.args...); e != nil {
			t.Fatal(e)
		}
	}
	before := f.snapshot(t)
	report, e := svc.Read(ctx, f.actor, id)
	if e != nil {
		t.Fatal(e)
	}
	if len(report.Rounds) != 1 || len(report.Rounds[0].Sources) != 1 || len(report.Rounds[0].Sources[0].Pages) != 1 {
		t.Fatalf("missing evidence %+v", report)
	}
	r := report.Rounds[0]
	if r.Reason != "review_required" || r.Sources[0].Pages[0].ReceiptPresent || r.Candidates[0].Reason != "subscription_blocked" {
		t.Fatalf("unsafe summary %+v", r)
	}
	raw, _ := json.Marshal(report)
	for _, secret := range []string{"secret.invalid", "private-guid", "private query", "private/", "passkey"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("leaked private material", string(raw))
		}
	}
	if f.snapshot(t) != before {
		t.Fatal("diagnostic read mutated business rows")
	}
	readToken:=*f.actor;readToken.Scopes=[]string{"read"}
	if _,e=svc.Read(ctx,&readToken,id);e!=nil{t.Fatal("read token rejected",e)}
	readToken.Scopes=[]string{"admin"}
	if _,e=svc.Read(ctx,&readToken,id);!errors.Is(e,ports.ErrForbidden){t.Fatal("token scope escalated",e)}
	if _, e = f.pool.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Read(ctx, f.actor, id); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("disabled reader", e)
	}
}
