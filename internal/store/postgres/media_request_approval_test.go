package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type approvalCreationFixture struct {
	creationFixture
	requests *postgres.MediaRequestStore
	admin    *ports.Actor
	in       ports.SubscriptionCreationInput
	prepared *ports.PreparedSubscriptionCreationMetadata
}

func newApprovalCreationFixture(t *testing.T, kind string) approvalCreationFixture {
	t.Helper()
	f := approvalCreationFixture{creationFixture: newCreationFixture(t, 117)}
	f.requests = postgres.NewMediaRequestStore(f.pool, idgen.New(), clock.NewMock(time.Now().Add(-time.Second)))
	f.admin = f.newAdmin(t)
	q := db.New(f.pool)
	ix, err := q.CreateIndexer(t.Context(), db.CreateIndexerParams{ID: uuid.New(), Name: "approval source", Url: "http://127.0.0.1", ApiKeyEncrypted: []byte("cipher"), Categories: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	dl, err := q.CreateDownloadClient(t.Context(), db.CreateDownloadClientParams{ID: uuid.New(), Name: "approval downloader", ClientType: "qbittorrent", Url: "http://127.0.0.1", PasswordEncrypted: []byte("cipher"), RemotePathMap: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	f.in = creationInput(9101, kind)
	owner := uuid.MustParse(f.actor.ID)
	f.in.UserID = &owner
	f.in.RootID = &f.movie
	f.in.IndexerIDs, f.in.DownloaderID = []uuid.UUID{ix.ID}, &dl.ID
	f.prepared = creationMovie(9101)
	if kind == "tv" {
		f.in.RootID = &f.tv
		f.in.Seasons = []int{1}
		f.prepared = creationTV(9101, creationSeason(1, 2, 7))
	}
	return f
}

func (f approvalCreationFixture) newAdmin(t *testing.T) *ports.Actor {
	t.Helper()
	a := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	if _, err := db.New(f.pool).CreateUser(t.Context(), db.CreateUserParams{ID: uuid.MustParse(a.ID), Username: "approval-admin-" + a.ID, PasswordHash: "fixture", Role: ports.RoleAdmin, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	return a
}

func (f approvalCreationFixture) submit(t *testing.T, key string) *ports.MediaRequestReceipt {
	t.Helper()
	r, err := f.requests.SubmitMediaRequest(t.Context(), f.actor, mediarequest.Intent{MediaType: f.in.MediaType, TMDBID: *f.in.TMDBID, Seasons: f.in.Seasons, IncludeSpecials: f.in.IncludeSpecials}, key)
	if err != nil || r == nil {
		t.Fatal("submit real owner request", r, err)
	}
	return r
}

func (f approvalCreationFixture) scoped(r *ports.MediaRequestReceipt, a *ports.Actor, key string) (creationFixture, string) {
	c := f.creationFixture
	c.actor = a
	c.store = postgres.NewMediaRequestApprovalCreationStore(f.pool, idgen.New(), ports.MediaRequestApprovalFence{RequestID: r.Request.ID, ExpectedVersion: 1, Key: key})
	return c, "media.request.approve:" + r.Request.ID.String() + ":" + key
}

func (f approvalCreationFixture) requestSnapshot(t *testing.T, r *ports.MediaRequestReceipt) string {
	t.Helper()
	var out string
	if err := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_array((SELECT to_jsonb(m) FROM media_request m WHERE id=$1),(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM app_user u))::text`, r.Request.ID).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return f.snapshot(t) + out
}

func (f approvalCreationFixture) originalRequest(t *testing.T, r *ports.MediaRequestReceipt) {
	t.Helper()
	got, err := f.requests.ReadMediaRequestReceipt(t.Context(), f.actor, r.CommandKey)
	mediaRequestWantReceipt(t, got, r, err)
	if got.Request.State != "pending" || got.Request.Version != 1 {
		t.Fatal("submission receipt lost its original pending snapshot", got)
	}
}

func (f approvalCreationFixture) pending(t *testing.T, r *ports.MediaRequestReceipt) {
	t.Helper()
	got, err := f.requests.ReadMediaRequest(t.Context(), f.admin, r.Request.ID)
	if err != nil || got == nil || !reflect.DeepEqual(got, &r.Request) {
		t.Fatal("failed approval changed request", got, err)
	}
	f.originalRequest(t, r)
}

func approvalWantBytes(t *testing.T, got, want *ports.SubscriptionCreationReceipt, err error) {
	t.Helper()
	if err != nil || got == nil || want == nil {
		t.Fatal("original creation receipt", got, err)
	}
	a, ae := json.Marshal(got)
	b, be := json.Marshal(want)
	if ae != nil || be != nil || !bytes.Equal(a, b) {
		t.Fatalf("creation receipt changed: got %s want %s (%v, %v)", a, b, ae, be)
	}
}

func (f approvalCreationFixture) noExecution(t *testing.T) {
	t.Helper()
	for _, table := range []string{"job", "download_task", "download_requirement", "unit_reservation", "acquisition_reservation", "library_file", "library_file_unit", "download_file_inventory"} {
		if n := intentCount(t, f.pool, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatal("approval created execution/file facts", table, n)
		}
	}
}

// All DDL belongs to this fixture's database. Cleanup remains usable after cancellation.
func (f approvalCreationFixture) trigger(t *testing.T, table, definition, body string) func() {
	t.Helper()
	execIntent(t, f.pool, `CREATE FUNCTION approval_test_hook() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+`; RETURN NEW; END $$`)
	drop := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if _, err := f.pool.Exec(ctx, "DROP TRIGGER IF EXISTS approval_test_hook ON "+table); err != nil {
			t.Error("drop owned approval trigger", err)
			return
		}
		if _, err := f.pool.Exec(ctx, "DROP FUNCTION IF EXISTS approval_test_hook()"); err != nil {
			t.Error("drop owned approval function", err)
		}
	}
	t.Cleanup(drop)
	execIntent(t, f.pool, definition+" EXECUTE FUNCTION approval_test_hook()")
	return drop
}

func TestMediaRequestApprovalActualCreationAndOriginalReceiptPG(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			f := newApprovalCreationFixture(t, kind)
			r := f.submit(t, "submit")
			// Exercise the full permitted client-key length separately from the creator prefix.
			c, key := f.scoped(r, f.admin, strings.Repeat("a", 80))
			beforeGuards := f.requestSnapshot(t, r)
			for _, mismatch := range []string{"owner", "tmdb", "scope", "specials"} {
				in := f.in
				switch mismatch {
				case "owner":
					id := uuid.MustParse(f.admin.ID)
					in.UserID = &id
				case "tmdb":
					id := *in.TMDBID + 1
					in.TMDBID = &id
				case "scope":
					in.ScopeType = "selected"
				case "specials":
					in.IncludeSpecials = !in.IncludeSpecials
				}
				_, cached, err := c.store.ReadSubscriptionCreationContext(t.Context(), c.actor, in, creationIdentity(key))
				if cached != nil || !errors.Is(err, ports.ErrMediaRequestConflict) || beforeGuards != f.requestSnapshot(t, r) {
					t.Fatal("derived request identity was not guarded before metadata preparation", mismatch, cached, err)
				}
			}
			out := c.create(t, f.in, f.prepared, key)
			wantUnits := 1
			wantKeys := []string{"movie"}
			if kind == "tv" {
				wantUnits, wantKeys = 2, []string{"S01E02", "S01E07"}
			}
			if out.Subscription.UserID.String() != f.actor.ID || out.Subscription.State != "active" || out.Subscription.IntentVersion != 1 || out.Subscription.RootID != *f.in.RootID || !reflect.DeepEqual(out.Subscription.IndexerIDs, f.in.IndexerIDs) || !reflect.DeepEqual(out.Subscription.DownloaderID, f.in.DownloaderID) || len(out.Wanted) != wantUnits {
				t.Fatal("actual owner intent/bindings", out)
			}
			for i, w := range out.Wanted {
				if w.UnitKey != wantKeys[i] || w.IsSkipped || w.IsSatisfied {
					t.Fatal("invented/excluded/satisfied unit", w)
				}
			}
			current, err := f.requests.ReadMediaRequest(t.Context(), f.admin, r.Request.ID)
			if err != nil || current == nil || current.State != "approved" || current.Version != 2 || current.SubscriptionID == nil || *current.SubscriptionID != out.Subscription.ID || current.DecidedBy == nil || current.DecidedBy.String() != f.admin.ID || current.OwnerID.String() != f.actor.ID {
				t.Fatal("approval link/version/actor", current, err)
			}
			for _, check := range []struct {
				query string
				count int
			}{
				{"SELECT count(*) FROM subscription", 1},
				{"SELECT count(*) FROM wanted_item", wantUnits},
				{"SELECT count(*) FROM media", 1},
				{"SELECT count(*) FROM media_unit", wantUnits},
				{"SELECT count(*) FROM audit_log", 3},
				{"SELECT count(*) FROM audit_log WHERE action='subscription.creation'", 1},
				{"SELECT count(*) FROM audit_log WHERE action='media_request.approved'", 1},
				{"SELECT count(*) FROM outbox_event", 3},
				{"SELECT count(*) FROM outbox_event WHERE event_type='subscription.created'", 1},
				{"SELECT count(*) FROM outbox_event WHERE event_type='media_request.approved'", 1},
				{"SELECT count(*) FROM idempotency_record", 2},
			} {
				if n := intentCount(t, f.pool, check.query); n != check.count {
					t.Fatal(check.query, n, "want", check.count)
				}
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM idempotency_record WHERE actor_id=$1 AND idempotency_key=$2 AND resource_id=$3`, f.admin.ID, "subscription.creation:"+key, out.Subscription.ID.String()); n != 1 {
				t.Fatal("normal creator original key missing", n)
			}
			f.noExecution(t)
			f.originalRequest(t, r)
			before := f.requestSnapshot(t, r)
			h, restored, err := c.store.ReadSubscriptionCreationContext(t.Context(), c.actor, f.in, creationIdentity(key))
			approvalWantBytes(t, restored, out, err)
			if !reflect.DeepEqual(h, ports.SubscriptionCreationContext{}) {
				t.Fatal("receipt recovery returned a metadata preparation hint", h)
			}
			restored, err = c.store.ReadSubscriptionCreationReceipt(t.Context(), c.actor, key)
			approvalWantBytes(t, restored, out, err)
			restored, err = c.store.CommitSubscriptionCreation(t.Context(), c.actor, f.in, h, nil, creationIdentity(key), func(ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
				t.Error("receipt recovery invoked creation decider")
				return ports.SubscriptionCreateDecision{}, ports.ErrUnavailable
			})
			approvalWantBytes(t, restored, out, err)
			if before != f.requestSnapshot(t, r) {
				t.Fatal("original receipt recovery wrote business data")
			}
		})
	}
}

func TestMediaRequestApprovalConcurrentApproversPG(t *testing.T) {
	f := newApprovalCreationFixture(t, "tv")
	r := f.submit(t, "submit")
	admins := []*ports.Actor{f.admin, f.newAdmin(t)}
	var creators [2]creationFixture
	var keys [2]string
	var hints [2]ports.SubscriptionCreationContext
	var results [2]*ports.SubscriptionCreationReceipt
	var errs [2]error
	for i := range creators {
		creators[i], keys[i] = f.scoped(r, admins[i], fmt.Sprintf("approve-%d", i))
		hints[i] = creators[i].read(t, f.in, creationIdentity(keys[i]))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range creators {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = creators[i].store.CommitSubscriptionCreation(ctx, admins[i], f.in, hints[i], f.prepared, creationIdentity(keys[i]), creationDecider(f.in, hints[i]))
		}(i)
	}
	close(start)
	wg.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil && results[i] != nil {
			if winner >= 0 {
				t.Fatal("two approvers won", results)
			}
			winner = i
		} else if !errors.Is(err, ports.ErrMediaRequestConflict) || results[i] != nil {
			t.Fatal("loser must receive request conflict, not unavailable", i, results[i], err)
		}
	}
	if winner < 0 {
		t.Fatal("no approval won", errs)
	}
	for _, check := range []struct {
		query string
		count int
	}{
		{"SELECT count(*) FROM subscription", 1},
		{"SELECT count(*) FROM wanted_item", 2},
		{"SELECT count(*) FROM audit_log", 3},
		{"SELECT count(*) FROM outbox_event", 3},
		{"SELECT count(*) FROM idempotency_record", 2},
		{"SELECT count(*) FROM media_request WHERE state='approved' AND version=2", 1},
	} {
		if n := intentCount(t, f.pool, check.query); n != check.count {
			t.Fatal("concurrent publication", check.query, n)
		}
	}
	before := f.requestSnapshot(t, r)
	got, err := creators[winner].store.ReadSubscriptionCreationReceipt(t.Context(), admins[winner], keys[winner])
	approvalWantBytes(t, got, results[winner], err)
	otherKeyCreator, otherKey := f.scoped(r, admins[winner], "terminal-second-key")
	_, got, err = otherKeyCreator.store.ReadSubscriptionCreationContext(t.Context(), admins[winner], f.in, creationIdentity(otherKey))
	if got != nil || !errors.Is(err, ports.ErrMediaRequestConflict) || before != f.requestSnapshot(t, r) {
		t.Fatal("terminal request adopted another approval key", got, err)
	}
	f.originalRequest(t, r)
	f.noExecution(t)
}

func TestMediaRequestApprovalMetadataWindowInvalidationPG(t *testing.T) {
	for _, mode := range []string{"owner-inactive", "admin-downgraded", "owner-withdraws", "admin-rejects"} {
		t.Run(mode, func(t *testing.T) {
			f := newApprovalCreationFixture(t, "tv")
			r := f.submit(t, "submit")
			c, key := f.scoped(r, f.admin, "approve")
			h := c.read(t, f.in, creationIdentity(key))
			want := ports.ErrForbidden
			switch mode {
			case "owner-inactive":
				execIntent(t, f.pool, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID))
			case "admin-downgraded":
				execIntent(t, f.pool, "UPDATE app_user SET role='member' WHERE id=$1", uuid.MustParse(f.admin.ID))
			default:
				a, action := f.actor, "withdraw"
				if mode == "admin-rejects" {
					a, action = f.admin, "reject"
				}
				if _, err := f.requests.CloseMediaRequest(t.Context(), a, r.Request.ID, ports.MediaRequestCloseInput{ExpectedVersion: 1, Action: action}, "close"); err != nil {
					t.Fatal(err)
				}
				want = ports.ErrMediaRequestConflict
			}
			before := f.requestSnapshot(t, r)
			// Prepared DTOs stand for metadata returned outside the transaction.
			out, err := c.store.CommitSubscriptionCreation(t.Context(), c.actor, f.in, h, f.prepared, creationIdentity(key), creationDecider(f.in, h))
			if out != nil || !errors.Is(err, want) || before != f.requestSnapshot(t, r) {
				t.Fatal("stale metadata window published or hid a conflict", out, err, "want", want)
			}
			_, cached, readErr := c.store.ReadSubscriptionCreationContext(t.Context(), c.actor, f.in, creationIdentity(key))
			if cached != nil || !errors.Is(readErr, want) {
				t.Fatal("pre-provider authorization accepted invalidated request", cached, readErr)
			}
			closed := mode == "owner-withdraws" || mode == "admin-rejects"
			wantCount := 1
			if closed {
				wantCount = 2
			}
			for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
				if n := intentCount(t, f.pool, "SELECT count(*) FROM "+table); n != wantCount {
					t.Fatal("request close evidence changed", table, n)
				}
			}
			for _, table := range []string{"subscription", "wanted_item", "media", "media_unit"} {
				if n := intentCount(t, f.pool, "SELECT count(*) FROM "+table); n != 0 {
					t.Fatal("invalidated approval left business rows", table, n)
				}
			}
			f.noExecution(t)
		})
	}
}

func TestMediaRequestApprovalTailRollbackPG(t *testing.T) {
	for _, stage := range []string{"request-audit", "request-outbox", "creator-idempotency", "approved-update", "deferred-constraint"} {
		t.Run(stage, func(t *testing.T) {
			f := newApprovalCreationFixture(t, "tv")
			r := f.submit(t, "submit")
			c, key := f.scoped(r, f.admin, "retry")
			h := c.read(t, f.in, creationIdentity(key))
			before := f.requestSnapshot(t, r)
			table, condition, operation := "media_request", "NEW.state='approved'", "BEFORE UPDATE"
			switch stage {
			case "request-audit":
				table, condition, operation = "audit_log", "NEW.action='media_request.approved'", "BEFORE INSERT"
			case "request-outbox":
				table, condition, operation = "outbox_event", "NEW.event_type='media_request.approved'", "BEFORE INSERT"
			case "creator-idempotency":
				table, condition, operation = "idempotency_record", "NEW.idempotency_key LIKE 'subscription.creation:media.request.approve:%'", "BEFORE INSERT"
			}
			definition := "CREATE TRIGGER approval_test_hook " + operation + " ON " + table + " FOR EACH ROW"
			if stage == "deferred-constraint" {
				definition = "CREATE CONSTRAINT TRIGGER approval_test_hook AFTER UPDATE ON media_request DEFERRABLE INITIALLY DEFERRED FOR EACH ROW"
			}
			drop := f.trigger(t, table, definition, "IF "+condition+" THEN RAISE EXCEPTION 'injected approval tail failure' USING ERRCODE='23514'; END IF")
			out, err := c.store.CommitSubscriptionCreation(t.Context(), c.actor, f.in, h, f.prepared, creationIdentity(key), creationDecider(f.in, h))
			if out != nil || !errors.Is(err, ports.ErrUnavailable) || before != f.requestSnapshot(t, r) {
				t.Fatal("approval tail escaped transaction rollback", out, err)
			}
			f.pending(t, r)
			f.noExecution(t)
			drop()
			out = c.create(t, f.in, f.prepared, key)
			if len(out.Wanted) != 2 || intentCount(t, f.pool, "SELECT count(*) FROM subscription") != 1 || intentCount(t, f.pool, "SELECT count(*) FROM idempotency_record") != 2 {
				t.Fatal("original-key retry did not publish exactly once", out)
			}
			f.originalRequest(t, r)
		})
	}
}

func TestMediaRequestApprovalReceiptMissingOrTamperedPG(t *testing.T) {
	for _, mode := range []string{"missing", "tampered"} {
		t.Run(mode, func(t *testing.T) {
			f := newApprovalCreationFixture(t, "movie")
			r := f.submit(t, "submit")
			c, key := f.scoped(r, f.admin, "approve")
			out := c.create(t, f.in, f.prepared, key)
			// An independently valid receipt must never be recovered through this fence.
			normal := f.creationFixture
			normal.actor = f.admin
			normal.create(t, f.in, nil, "unrelated")
			before := f.requestSnapshot(t, r)
			got, err := c.store.ReadSubscriptionCreationReceipt(t.Context(), c.actor, "unrelated")
			if got != nil || !errors.Is(err, ports.ErrMediaRequestConflict) {
				t.Fatal("scoped receipt recovered unrelated creator key", got, err)
			}
			wrong, wrongKey := f.scoped(r, f.admin, "different")
			_, got, err = wrong.store.ReadSubscriptionCreationContext(t.Context(), wrong.actor, f.in, creationIdentity(wrongKey))
			if got != nil || !errors.Is(err, ports.ErrMediaRequestConflict) {
				t.Fatal("terminal request adopted wrong key", got, err)
			}
			if before != f.requestSnapshot(t, r) {
				t.Fatal("wrong-key receipt lookup wrote business data")
			}
			second := f.submit(t, "second-request")
			wrongRequest, _ := f.scoped(second, f.admin, "approve")
			got, err = wrongRequest.store.ReadSubscriptionCreationReceipt(t.Context(), f.admin, key)
			if got != nil || !errors.Is(err, ports.ErrMediaRequestConflict) {
				t.Fatal("wrong request recovered another request's subscription", got, err)
			}
			// The second submission is legitimate; freeze the graph after that write.
			before = f.requestSnapshot(t, r)
			query := "DELETE FROM idempotency_record WHERE actor_id=$1 AND idempotency_key=$2"
			if mode == "tampered" {
				query = `UPDATE idempotency_record SET response_body=jsonb_set(response_body,'{result_hash}',to_jsonb(repeat('0',64))) WHERE actor_id=$1 AND idempotency_key=$2`
			}
			tag, mutationErr := f.pool.Exec(t.Context(), query, f.admin.ID, "subscription.creation:"+key)
			if mutationErr != nil {
				var pgErr *pgconn.PgError
				if !errors.As(mutationErr, &pgErr) || pgErr.Code != "23514" || before != f.requestSnapshot(t, r) {
					t.Fatal("unexpected tamper rejection", mutationErr)
				}
				got, err = c.store.ReadSubscriptionCreationReceipt(t.Context(), c.actor, key)
				approvalWantBytes(t, got, out, err)
				return
			}
			if tag.RowsAffected() != 1 {
				t.Fatal("did not mutate exactly the owned original receipt", tag)
			}
			before = f.requestSnapshot(t, r)
			_, got, err = c.store.ReadSubscriptionCreationContext(t.Context(), c.actor, f.in, creationIdentity(key))
			if got != nil || mode == "missing" && !errors.Is(err, ports.ErrUnavailable) || mode == "tampered" && !errors.Is(err, ports.ErrUnavailable) && !errors.Is(err, ports.ErrSubscriptionBindingsConflict) {
				t.Fatal("broken original receipt allowed metadata preparation", got, err)
			}
			got, err = c.store.CommitSubscriptionCreation(t.Context(), c.actor, f.in, ports.SubscriptionCreationContext{}, nil, creationIdentity(key), func(ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
				t.Error("broken receipt attempted creation again")
				return ports.SubscriptionCreateDecision{}, ports.ErrUnavailable
			})
			if err == nil || got != nil || before != f.requestSnapshot(t, r) {
				t.Fatal("broken receipt was repaired or resent", got, err)
			}
			f.originalRequest(t, r)
			f.noExecution(t)
		})
	}
}

func TestMediaRequestApprovalExistingPausedIntentPG(t *testing.T) {
	for _, mode := range []string{"matching", "scope-conflict", "binding-conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := newApprovalCreationFixture(t, "tv")
			initialInput := f.in
			if mode == "scope-conflict" {
				initialInput.ScopeType = "selected"
				initialInput.EpisodeKeys = []string{"S01E07"}
			}
			if mode == "binding-conflict" {
				dl, err := db.New(f.pool).CreateDownloadClient(t.Context(), db.CreateDownloadClientParams{ID: uuid.New(), Name: "other downloader", ClientType: "qbittorrent", Url: "http://127.0.0.1", PasswordEncrypted: []byte("cipher"), RemotePathMap: []byte("[]")})
				if err != nil {
					t.Fatal(err)
				}
				initialInput.DownloaderID = &dl.ID
			}
			first := f.create(t, initialInput, f.prepared, "normal-first")
			execIntent(t, f.pool, "UPDATE subscription SET state='paused' WHERE id=$1", first.Subscription.ID)
			execIntent(t, f.pool, "UPDATE wanted_item SET user_skipped=true,is_skipped=true WHERE subscription_id=$1", first.Subscription.ID)
			r := f.submit(t, "submit")
			c, key := f.scoped(r, f.admin, "approve")
			h := c.read(t, f.in, creationIdentity(key))
			if h.Existing == nil || h.Existing.State != "paused" {
				t.Fatal("pre-I/O observation lost paused intent", h)
			}
			before := f.requestSnapshot(t, r)
			var wantedBefore string
			wantedSQL := `SELECT jsonb_agg(jsonb_build_array(id,subscription_id,unit_id,intent_version,user_skipped,scope_excluded,is_skipped,is_satisfied) ORDER BY id)::text FROM wanted_item`
			if err := f.pool.QueryRow(t.Context(), wantedSQL).Scan(&wantedBefore); err != nil {
				t.Fatal(err)
			}
			got, err := c.store.CommitSubscriptionCreation(t.Context(), c.actor, f.in, h, nil, creationIdentity(key), creationDecider(f.in, h))
			if mode != "matching" {
				if got != nil || !errors.Is(err, ports.ErrSubscriptionBindingsConflict) || before != f.requestSnapshot(t, r) {
					t.Fatal("approval overwrote existing scope/bindings", got, err)
				}
				f.pending(t, r)
				return
			}
			if err != nil || got == nil || !reflect.DeepEqual(got.Subscription, *h.Existing) || got.Subscription.ID != first.Subscription.ID || got.Subscription.IntentVersion != first.Subscription.IntentVersion || len(got.Wanted) != 2 {
				t.Fatal("approval activated or rebound paused intent", got, err)
			}
			var wantedAfter string
			if err := f.pool.QueryRow(t.Context(), wantedSQL).Scan(&wantedAfter); err != nil || wantedBefore != wantedAfter {
				t.Fatal("paused Wanted identity/version/user flags changed", wantedBefore, wantedAfter, err)
			}
			for _, w := range got.Wanted {
				if !w.IsSkipped {
					t.Fatal("approval cleared user's skipped Wanted", w)
				}
			}
			if intentCount(t, f.pool, "SELECT count(*) FROM subscription") != 1 || intentCount(t, f.pool, "SELECT count(*) FROM wanted_item") != 2 || intentCount(t, f.pool, "SELECT count(*) FROM media_request WHERE state='approved' AND version=2") != 1 {
				t.Fatal("paused intent not reused exactly once")
			}
			f.originalRequest(t, r)
			f.noExecution(t)
		})
	}
}

func TestMediaRequestApprovalDeferredPermissionRollbackPG(t *testing.T) {
	for _, account := range []string{"admin", "owner"} {
		t.Run(account, func(t *testing.T) {
			f := newApprovalCreationFixture(t, "tv")
			r := f.submit(t, "submit")
			c, key := f.scoped(r, f.admin, "retry")
			h := c.read(t, f.in, creationIdentity(key))
			before := f.requestSnapshot(t, r)
			body := fmt.Sprintf("UPDATE app_user SET role='member' WHERE id='%s'::uuid", f.admin.ID)
			if account == "owner" {
				body = fmt.Sprintf("UPDATE app_user SET is_active=false WHERE id='%s'::uuid", f.actor.ID)
			}
			drop := f.trigger(t, "media_request", "CREATE CONSTRAINT TRIGGER approval_test_hook AFTER UPDATE ON media_request DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.state='approved')", body)
			out, err := c.store.CommitSubscriptionCreation(t.Context(), c.actor, f.in, h, f.prepared, creationIdentity(key), creationDecider(f.in, h))
			// Forbidden specifically proves the deferred mutation ran before live authorization.
			if out != nil || !errors.Is(err, ports.ErrForbidden) || before != f.requestSnapshot(t, r) {
				t.Fatal("deferred permission revocation did not roll back the complete approval/account transaction", out, err)
			}
			f.pending(t, r)
			f.noExecution(t)
			drop()
			out = c.create(t, f.in, f.prepared, key)
			if len(out.Wanted) != 2 || intentCount(t, f.pool, "SELECT count(*) FROM subscription") != 1 || intentCount(t, f.pool, "SELECT count(*) FROM idempotency_record") != 2 {
				t.Fatal("same-key retry after deferred rollback failed", out)
			}
			f.originalRequest(t, r)
		})
	}
}
