package postgres_test

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

type personCreationFixture struct {
	personSourceFixture
	history126 string
	permit     ports.PersonFollowSyncPermit
	item       ports.PersonFollowSyncItem
	fence      ports.PersonFollowCreationFence
	input      ports.SubscriptionCreationInput
	provider   *wishCreationProvider
}

func newPersonCreationFixture(t *testing.T) *personCreationFixture {
	t.Helper()
	source := newPersonSourceFixture(t)
	history := repositoryMigrationsThrough(t, 126)
	if version, err := postgres.Migrate(t.Context(), source.p.Config().ConnString(), history); err != nil || version != 126 {
		t.Fatal(version, err)
	}
	f := &personCreationFixture{personSourceFixture: source, history126: history, provider: &wishCreationProvider{}}
	f.prepare(t)
	return f
}
func (f *personCreationFixture) prepare(t *testing.T) {
	t.Helper()
	f.permit = f.acquire(t)
	if err := f.permit.StoreCredits(t.Context(), f.credits(1)); err != nil {
		t.Fatal(err)
	}
	items, err := f.permit.Items(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	f.item = items[0]
	work := f.permit.Snapshot()
	f.fence = ports.PersonFollowCreationFence{Lease: f.lease, RunID: work.RunID, ExecutionID: work.ExecutionID, ConfigID: work.Config.ID, ConfigVersion: work.Config.Version, ItemID: f.item.ID}
	tmdb, root := f.item.Observation.TMDBID, f.item.RootID
	f.input = ports.SubscriptionCreationInput{TMDBID: &tmdb, MediaType: f.item.Observation.MediaType, RootID: &root, QualityPolicyID: work.Config.Settings.QualityPolicyID, ScopeType: "all"}
}
func (f *personCreationFixture) creator() *subscription.SubscriptionCreationService {
	return subscription.NewSubscriptionCreationService(postgres.NewPersonFollowCreationStore(f.p, idgen.New(), f.fence), f.provider, f.provider, artworkCreationSecrets{})
}
func (f *personCreationFixture) create(t *testing.T) *ports.SubscriptionCreationReceipt {
	t.Helper()
	result, err := f.creator().Create(t.Context(), f.actor, f.input, f.item.CommandKey)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestPersonFollowCreationAtomicAndOriginalReceiptPG(t *testing.T) {
	f := newPersonCreationFixture(t)
	before := f.counts(t)
	result := f.create(t)
	if result.Subscription.ID == uuid.Nil || result.Subscription.UserID != uuid.MustParse(f.actor.ID) || result.Subscription.RootID != f.root || len(result.Wanted) != 1 {
		t.Fatal("creation lost selected owner, root or Wanted")
	}
	if !reflect.DeepEqual(f.sourceCounts(t), []int{1, 1, 1}) {
		t.Fatal("successful creation did not commit candidate ledger")
	}
	after := f.counts(t)
	if after[2] != before[2]+1 || after[3] != before[3]+1 || after[4] != before[4]+1 || after[6] != before[6]+1 || after[7] != before[7]+1 || after[8] != before[8] || after[9] != before[9] {
		t.Fatal("creation did not commit exactly one business result", before, after)
	}
	reads := f.provider.calls.Load()
	again := f.create(t)
	if !reflect.DeepEqual(result, again) || f.provider.calls.Load() != reads || !reflect.DeepEqual(after, f.counts(t)) {
		t.Fatal("original-key replay repeated metadata or accounting")
	}
	if err := f.permit.FinishPage(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.permit.Close()
	f.exec(t, `UPDATE library_root SET health='offline' WHERE id=$1`, f.root)
	public := subscription.NewSubscriptionCreationService(postgres.NewSubscriptionCreationStore(f.p, idgen.New()), nil, nil, artworkCreationSecrets{})
	restored, err := public.Receipt(t.Context(), f.actor, f.item.CommandKey)
	if err != nil || !reflect.DeepEqual(result, restored) || !reflect.DeepEqual(after, f.counts(t)) || f.provider.calls.Load() != reads {
		t.Fatal("offline historical receipt recomputed the result", err)
	}
	t.Log("M11_PERSON_CREATION_ATOMIC_PASS source=VERIFIED subscription=WANTED_ATOMIC original_key=OFFLINE_READ_ONLY download_tasks=ZERO")
}
func TestPersonFollowCreationConcurrentPG(t *testing.T) {
	f := newPersonCreationFixture(t)
	before := f.counts(t)
	var wg sync.WaitGroup
	results := make(chan *ports.SubscriptionCreationReceipt, 6)
	failures := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := f.creator().Create(t.Context(), f.actor, f.input, f.item.CommandKey)
			if err != nil {
				failures <- err
				return
			}
			results <- receipt
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var original *ports.SubscriptionCreationReceipt
	for receipt := range results {
		if original == nil {
			original = receipt
		} else if !reflect.DeepEqual(original, receipt) {
			t.Fatal("concurrent commands returned different results")
		}
	}
	after := f.counts(t)
	for _, i := range []int{2, 3, 4, 6, 7} {
		if after[i] != before[i]+1 {
			t.Fatal("concurrent creation duplicated accounting", i, before, after)
		}
	}
	if !reflect.DeepEqual(f.sourceCounts(t), []int{1, 1, 1}) {
		t.Fatal("concurrent creation lost candidate result")
	}
}
func TestPersonFollowCreationMissingReceiptAndForgedScopePG(t *testing.T) {
	f := newPersonCreationFixture(t)
	for _, change := range []func(*ports.PersonFollowCreationFence){func(f *ports.PersonFollowCreationFence) { f.ConfigVersion++ }, func(f *ports.PersonFollowCreationFence) { f.ItemID = uuid.New() }, func(f *ports.PersonFollowCreationFence) { f.ExecutionID = uuid.New() }, func(f *ports.PersonFollowCreationFence) { f.Lease.Token++ }} {
		fence := f.fence
		change(&fence)
		service := subscription.NewSubscriptionCreationService(postgres.NewPersonFollowCreationStore(f.p, idgen.New(), fence), f.provider, f.provider, artworkCreationSecrets{})
		if _, err := service.Create(t.Context(), f.actor, f.input, f.item.CommandKey); err == nil {
			t.Fatal("forged execution scope created subscription")
		}
	}
	if f.provider.calls.Load() != 0 || !reflect.DeepEqual(f.sourceCounts(t), []int{1, 1, 0}) {
		t.Fatal("unproven fence reached metadata or created results")
	}
	f.create(t)
	reads := f.provider.calls.Load()
	f.exec(t, `DELETE FROM idempotency_record WHERE idempotency_key=$1`, "subscription.creation:"+f.item.CommandKey)
	before := f.counts(t)
	if _, err := f.creator().Create(t.Context(), f.actor, f.input, f.item.CommandKey); !errors.Is(err, ports.ErrSubscriptionBindingsConflict) {
		t.Fatal("missing original creation receipt reconstructed success", err)
	}
	if f.provider.calls.Load() != reads || !reflect.DeepEqual(before, f.counts(t)) {
		t.Fatal("missing original result repeated metadata or accounting")
	}
}
func TestPersonFollowCreationTailRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record", "person_follow_sync_item"} {
		t.Run(table, func(t *testing.T) {
			f := newPersonCreationFixture(t)
			before := f.counts(t)
			operation := "INSERT"
			if table == "person_follow_sync_item" {
				operation = "UPDATE"
			}
			f.exec(t, `CREATE FUNCTION person_creation_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned private creation'; END $$`)
			f.exec(t, "CREATE TRIGGER person_creation_tail BEFORE "+operation+" ON "+table+" FOR EACH ROW EXECUTE FUNCTION person_creation_tail()")
			if _, err := f.creator().Create(t.Context(), f.actor, f.input, f.item.CommandKey); err == nil {
				t.Fatal("tail failure became success")
			}
			if !reflect.DeepEqual(before, f.counts(t)) || !reflect.DeepEqual(f.sourceCounts(t), []int{1, 1, 0}) {
				t.Fatal("creation did not fully roll back")
			}
			f.exec(t, "DROP TRIGGER person_creation_tail ON "+table)
			f.create(t)
		})
	}
}
func TestPersonFollowCreationSameWorkNewRunPreservesPausedPG(t *testing.T) {
	f := newPersonCreationFixture(t)
	original := f.create(t)
	reads := f.provider.calls.Load()
	if err := f.permit.FinishPage(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.permit.Close()
	f.exec(t, `UPDATE subscription SET state='paused',intent_version=intent_version+1,updated_at=clock_timestamp() WHERE id=$1`, original.Subscription.ID)
	before := f.counts(t)
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	q := db.New(tx)
	history, err := q.ReadPersonFollowVersion(t.Context(), db.ReadPersonFollowVersionParams{ConfigID: f.id, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	run, job := uuid.New(), uuid.New()
	if err = q.InsertPersonFollowRun(t.Context(), db.InsertPersonFollowRunParams{ID: run, ConfigID: f.id, ConfigVersion: 1, OwnerID: uuid.MustParse(f.actor.ID), ConfigSnapshot: history.Snapshot}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"version":1,"run_id":%q,"start":0}`, run))
	if err = q.InsertPersonFollowSyncJob(t.Context(), db.InsertPersonFollowSyncJobParams{ID: job, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err = q.InsertPersonFollowSyncPage(t.Context(), db.InsertPersonFollowSyncPageParams{JobID: job, RunID: run, PageStart: 0}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE job SET state='running',lease_owner='person-worker',lease_token=1,lease_until=clock_timestamp()+interval '5 minutes',attempts=1 WHERE id=$1`, job)
	f.lease = ports.JobLease{ID: job, Owner: "person-worker", Token: 1}
	f.prepare(t)
	restored := f.create(t)
	if !reflect.DeepEqual(original, restored) || f.provider.calls.Load() != reads {
		t.Fatal("later run recomputed original result")
	}
	var status string
	if err = f.p.QueryRow(t.Context(), `SELECT state FROM subscription WHERE id=$1`, original.Subscription.ID).Scan(&status); err != nil || status != "paused" {
		t.Fatal("automatic follow changed paused intent", status, err)
	}
	after := f.counts(t)
	for _, i := range []int{0, 1, 2, 3, 4, 6, 7, 8, 9} {
		if before[i] != after[i] {
			t.Fatal("later run duplicated original business result", i)
		}
	}
	if !reflect.DeepEqual(f.sourceCounts(t), []int{2, 2, 2}) {
		t.Fatal("later source not bound to original success")
	}
}
func TestPersonFollowCreationSchema126HistoryPG(t *testing.T) {
	f := newPersonCreationFixture(t)
	if version, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history126); err != nil || version != 125 {
		t.Fatal("empty creation downgrade", version, err)
	}
	before := f.counts(t)
	if _, err := f.creator().Create(t.Context(), f.actor, f.input, f.item.CommandKey); err == nil {
		t.Fatal("old source contract authorized independent creation")
	}
	if !reflect.DeepEqual(before, f.counts(t)) || !reflect.DeepEqual(f.sourceCounts(t), []int{1, 1, 0}) {
		t.Fatal("old contract rejection partially committed")
	}
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), f.history126); err != nil || version != 126 {
		t.Fatal(version, err)
	}
	f.create(t)
	if _, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history126); err == nil {
		t.Fatal("downgrade discarded created-candidate contract")
	}
	if !reflect.DeepEqual(f.sourceCounts(t), []int{1, 1, 1}) {
		t.Fatal("creation protection damaged original result")
	}
}
