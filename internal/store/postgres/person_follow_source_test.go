package postgres_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type personSourceFixture struct {
	personExecutionFixture
	history125 string
}

func newPersonSourceFixture(t *testing.T) personSourceFixture {
	t.Helper()
	f := newPersonExecutionFixture(t)
	history := repositoryMigrationsThrough(t, 125)
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), history); err != nil || version != 125 {
		t.Fatal(version, err)
	}
	return personSourceFixture{f, history}
}
func (f personSourceFixture) acquire(t *testing.T) ports.PersonFollowSyncPermit {
	t.Helper()
	permit, err := f.scheduler.Acquire(t.Context(), f.lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(permit.Close)
	return permit
}
func (f personSourceFixture) credits(count int) ports.PersonCredits {
	result := ports.PersonCredits{PersonID: f.input().Settings.PersonID, Role: f.input().Settings.Role, Works: make([]ports.PersonWork, 0, count)}
	for i := 0; i < count; i++ {
		result.Works = append(result.Works, ports.PersonWork{TMDBID: int64(550 + i), MediaType: "movie", Title: fmt.Sprintf("Real movie %d", i), ReleaseDate: f.input().Settings.MinReleaseDate})
	}
	return result
}
func (f personSourceFixture) sourceCounts(t *testing.T) []int {
	t.Helper()
	var sources, items, created int
	if err := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM person_follow_sync_source),(SELECT count(*) FROM person_follow_sync_item),(SELECT count(*) FROM person_follow_sync_item WHERE state='created')`).Scan(&sources, &items, &created); err != nil {
		t.Fatal(err)
	}
	return []int{sources, items, created}
}
func TestPersonFollowSourceSealedAndResumePG(t *testing.T) {
	f := newPersonSourceFixture(t)
	before := f.counts(t)
	permit := f.acquire(t)
	credits := f.credits(3)
	credits.Works[1].ReleaseDate = ""
	credits.Works[2].ReleaseDate = "1800-01-01"
	if err := permit.StoreCredits(t.Context(), credits); err != nil {
		t.Fatal(err)
	}
	if err := permit.StoreCredits(t.Context(), credits); err != nil {
		t.Fatal("same source was not idempotent", err)
	}
	items, err := permit.Items(t.Context())
	if err != nil || len(items) != 3 {
		t.Fatal(items, err)
	}
	states := map[int64]string{}
	for _, item := range items {
		states[item.Observation.TMDBID] = item.State
	}
	if states[550] != "pending" || states[551] != "review_required" || states[552] != "excluded" {
		t.Fatal("classification changed date meaning", states)
	}
	if err := permit.FinishPage(t.Context()); !errors.Is(err, ports.ErrPersonFollowConflict) {
		t.Fatal("pending work completed a page", err)
	}
	permit.Close()
	f.lease = f.nextLease(t)
	resumed := f.acquire(t)
	if !resumed.Snapshot().SourceStored {
		t.Fatal("sealed source not bound to resumed execution")
	}
	again, err := resumed.Items(t.Context())
	if err != nil || !reflect.DeepEqual(items, again) {
		t.Fatal("resume reread or changed evidence", err)
	}
	credits.Works[0].Title = "Different response"
	if err := resumed.StoreCredits(t.Context(), credits); !errors.Is(err, ports.ErrPersonFollowConflict) {
		t.Fatal("new response replaced sealed evidence", err)
	}
	if err := resumed.ReviewItem(t.Context(), items[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := resumed.FinishPage(t.Context()); err != nil {
		t.Fatal(err)
	}
	resumed.Close()
	if !reflect.DeepEqual(f.sourceCounts(t), []int{1, 3, 0}) || !reflect.DeepEqual(before, f.counts(t)) {
		t.Fatal("source processing modified subscription or ingestion ledgers")
	}
	var status string
	if err := f.p.QueryRow(t.Context(), `SELECT status FROM person_follow_sync_run`).Scan(&status); err != nil || status != "partial" {
		t.Fatal("review state lost", status, err)
	}
}
func TestPersonFollowSource65ItemsPaginationPG(t *testing.T) {
	f := newPersonSourceFixture(t)
	before := f.counts(t)
	first := f.acquire(t)
	if err := first.StoreCredits(t.Context(), f.credits(65)); err != nil {
		t.Fatal(err)
	}
	page1, err := first.Items(t.Context())
	if err != nil || len(page1) != 64 {
		t.Fatal("first work page", len(page1), err)
	}
	for _, item := range page1 {
		if err := first.ReviewItem(t.Context(), item.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.FinishPage(t.Context()); err != nil {
		t.Fatal(err)
	}
	first.Close()
	var job uuid.UUID
	if err := f.p.QueryRow(t.Context(), `UPDATE job SET state='running',lease_owner='person-worker',lease_token=1,lease_until=clock_timestamp()+interval '5 minutes',attempts=1 WHERE kind='person_follow_sync' AND state='queued' RETURNING id`).Scan(&job); err != nil {
		t.Fatal(err)
	}
	f.lease = ports.JobLease{ID: job, Owner: "person-worker", Token: 1}
	second := f.acquire(t)
	if !second.Snapshot().SourceStored || second.Snapshot().Start != 64 {
		t.Fatal("continuation lost original source")
	}
	page2, err := second.Items(t.Context())
	if err != nil || len(page2) != 1 {
		t.Fatal("last work page", len(page2), err)
	}
	seen := map[uuid.UUID]bool{}
	for _, item := range append(page1, page2...) {
		if seen[item.ID] {
			t.Fatal("duplicate item across pages")
		}
		seen[item.ID] = true
	}
	if len(seen) != 65 {
		t.Fatal("missing source item")
	}
	if err := second.ReviewItem(t.Context(), page1[0].ID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("continuation can change an earlier page", err)
	}
	if err := second.ReviewItem(t.Context(), page2[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := second.FinishPage(t.Context()); err != nil {
		t.Fatal(err)
	}
	second.Close()
	var exact bool
	if err := f.p.QueryRow(t.Context(), `SELECT count(*)=2 AND bool_and(p.finished AND j.state='succeeded' AND j.payload=jsonb_build_object('version',1,'run_id',p.run_id,'start',p.page_start)) FROM person_follow_sync_page p JOIN job j ON j.id=p.job_id`).Scan(&exact); err != nil || !exact {
		t.Fatal("continuation dispatch mismatch", err)
	}
	after := f.counts(t)
	for _, i := range []int{0, 1, 2, 3, 4, 6, 7, 8, 9} {
		if before[i] != after[i] {
			t.Fatal("work pagination changed protected ledgers", i)
		}
	}
	if !reflect.DeepEqual(f.sourceCounts(t), []int{1, 65, 0}) {
		t.Fatal("pagination changed sealed work count")
	}
	t.Log("M11_PERSON_SOURCE_65_PASS source=SEALED pages=64+1 identity=PRESERVED metadata_reread=ZERO subscriptions=ZERO")
}
func TestPersonFollowSourceInvalidAndEmptyPG(t *testing.T) {
	f := newPersonSourceFixture(t)
	permit := f.acquire(t)
	for _, mutate := range []func(*ports.PersonCredits){func(c *ports.PersonCredits) { c.PersonID++ }, func(c *ports.PersonCredits) { c.Role = "unknown" }, func(c *ports.PersonCredits) { c.Works = nil }, func(c *ports.PersonCredits) { c.Works[0].ReleaseDate = "2026-02-30" }, func(c *ports.PersonCredits) {
		other := c.Works[0]
		other.Title = "Conflicting"
		c.Works = append(c.Works, other)
	}} {
		credits := f.credits(1)
		mutate(&credits)
		if err := permit.StoreCredits(t.Context(), credits); !errors.Is(err, ports.ErrBadResponse) {
			t.Fatal("invalid source accepted", err)
		}
	}
	if !reflect.DeepEqual(f.sourceCounts(t), []int{0, 0, 0}) {
		t.Fatal("invalid source made partial evidence")
	}
	if err := permit.StoreCredits(t.Context(), f.credits(0)); err != nil {
		t.Fatal("empty success became failure", err)
	}
	items, err := permit.Items(t.Context())
	if err != nil || items == nil || len(items) != 0 {
		t.Fatal("empty source contract", err)
	}
	if err := permit.FinishPage(t.Context()); err != nil {
		t.Fatal(err)
	}
	permit.Close()
	var status string
	if err := f.p.QueryRow(t.Context(), `SELECT status FROM person_follow_sync_run`).Scan(&status); err != nil || status != "succeeded" {
		t.Fatal("empty result state", status, err)
	}
}
func TestPersonFollowSourceTailRollbackPG(t *testing.T) {
	for _, table := range []string{"person_follow_sync_source", "person_follow_sync_item"} {
		t.Run(table, func(t *testing.T) {
			f := newPersonSourceFixture(t)
			permit := f.acquire(t)
			before := f.counts(t)
			f.exec(t, `CREATE FUNCTION person_source_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned private source'; END $$`)
			f.exec(t, "CREATE TRIGGER person_source_tail BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION person_source_tail()")
			if err := permit.StoreCredits(t.Context(), f.credits(2)); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("tail failure not classified", err)
			}
			if !reflect.DeepEqual(f.sourceCounts(t), []int{0, 0, 0}) || !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("source failed to roll back")
			}
			f.exec(t, "DROP TRIGGER person_source_tail ON "+table)
			if err := permit.StoreCredits(t.Context(), f.credits(2)); err != nil {
				t.Fatal("tail rollback consumed source permit", err)
			}
		})
	}
}
func TestPersonFollowSourceCommitAuthorityPG(t *testing.T) {
	for index, mutation := range []string{"UPDATE job SET lease_until=clock_timestamp()-interval '1 second'", "UPDATE person_follow_config SET enabled=false,version=version+1,updated_at=clock_timestamp()", "UPDATE app_user SET is_active=false"} {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			f := newPersonSourceFixture(t)
			permit := f.acquire(t)
			before := f.counts(t)
			f.exec(t, `CREATE FUNCTION person_source_late() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+mutation+`; RETURN NEW; END $$`)
			f.exec(t, `CREATE TRIGGER person_source_late AFTER INSERT ON person_follow_sync_item FOR EACH ROW EXECUTE FUNCTION person_source_late()`)
			if err := permit.StoreCredits(t.Context(), f.credits(1)); err == nil {
				t.Fatal("late authority committed a source")
			}
			if !reflect.DeepEqual(f.sourceCounts(t), []int{0, 0, 0}) || !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("late source rejection left writes")
			}
			f.exec(t, `DROP TRIGGER person_source_late ON person_follow_sync_item`)
			if err := permit.StoreCredits(t.Context(), f.credits(1)); err != nil {
				t.Fatal("late rejection consumed source permit", err)
			}
		})
	}
}
func TestPersonFollowSourceSchema125HistoryPG(t *testing.T) {
	f := newPersonSourceFixture(t)
	if version, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history125); err != nil || version != 124 {
		t.Fatal("empty source downgrade", version, err)
	}
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), f.history125); err != nil || version != 125 {
		t.Fatal(version, err)
	}
	permit := f.acquire(t)
	if err := permit.StoreCredits(t.Context(), f.credits(1)); err != nil {
		t.Fatal(err)
	}
	permit.Close()
	for _, query := range []string{`UPDATE person_follow_sync_source SET source='{}'::jsonb`, `DELETE FROM person_follow_sync_source`, `TRUNCATE person_follow_sync_source CASCADE`, `UPDATE person_follow_sync_item SET ordinal=ordinal+1`, `DELETE FROM person_follow_sync_item`} {
		_, err := f.p.Exec(t.Context(), query)
		var storageError *pgconn.PgError
		if !errors.As(err, &storageError) || storageError.Code != "55000" {
			t.Fatal("source history protection not enforced", err)
		}
	}
	if _, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history125); err == nil {
		t.Fatal("downgrade discarded source evidence")
	}
	if !reflect.DeepEqual(f.sourceCounts(t), []int{1, 1, 0}) {
		t.Fatal("history protection damaged original source")
	}
}
