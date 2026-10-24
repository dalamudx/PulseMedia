package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/personfollow"
	"github.com/google/uuid"
)

type personFollowFixture struct {
	endpointConfigFixture
	history string
	root    uuid.UUID
	store   *postgres.PersonFollowStore
	service *personfollow.ConfigService
}

func newPersonFollowFixture(t *testing.T) personFollowFixture {
	t.Helper()
	f := newEndpointConfigFixture(t)
	history := repositoryMigrationsThrough(t, 122)
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), history); err != nil || version != 122 {
		t.Fatal(version, err)
	}
	root := uuid.New()
	f.exec(t, `INSERT INTO library_root(id,path,media_type,name,health)VALUES($1,$2,'movie','Follow target','online')`, root, t.TempDir())
	f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, f.actor.ID)
	f.actor.Role = ports.RoleMember
	ids := idgen.New()
	store := postgres.NewPersonFollowStore(f.p, ids, outbox.New(f.p, ids, clock.New()))
	return personFollowFixture{f, history, root, store, personfollow.NewConfigService(store)}
}
func (f personFollowFixture) input() ports.PersonFollowInput {
	root := f.root
	return ports.PersonFollowInput{Settings: domain.Settings{PersonID: 525, Role: "director", Enabled: true, MinReleaseDate: "2026-10-01", IntervalMinutes: 1440, MovieRootID: &root, QualityPolicyID: "default", QualityPolicyRevision: 1}}
}
func (f personFollowFixture) save(t *testing.T, id uuid.UUID, in ports.PersonFollowInput, key string) *ports.PersonFollowReceipt {
	t.Helper()
	r, err := f.service.Save(t.Context(), f.actor, id, in, key)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f personFollowFixture) counts(t *testing.T) []int64 {
	t.Helper()
	counts := make([]int64, 10)
	err := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM person_follow_config),(SELECT count(*) FROM person_follow_config_history),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM idempotency_record),(SELECT count(*) FROM job),(SELECT count(*) FROM subscription),(SELECT count(*) FROM wanted_item),(SELECT count(*) FROM download_task),(SELECT count(*) FROM outbox_delivery)`).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6], &counts[7], &counts[8], &counts[9])
	if err != nil {
		t.Fatal(err)
	}
	return counts
}
func (f personFollowFixture) bindings(t *testing.T) *domain.Bindings {
	t.Helper()
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	for _, id := range ids {
		f.exec(t, `INSERT INTO indexer(id,name,url,api_key_encrypted,health)VALUES($1,'Follow indexer','https://fixture.invalid','fixture','online')`, id)
		f.exec(t, `INSERT INTO indexer_lifecycle(indexer_id,enabled)VALUES($1,true)`, id)
	}
	downloader := uuid.New()
	f.exec(t, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted)VALUES($1,'Follow downloader','qbittorrent','https://fixture.invalid','fixture','fixture')`, downloader)
	return &domain.Bindings{IndexerIDs: ids, DownloaderID: &downloader}
}
func samePersonFollow(a, b ports.PersonFollowConfig) bool {
	return a.ID == b.ID && a.OwnerID == b.OwnerID && a.Version == b.Version && a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt) && reflect.DeepEqual(a.Settings, b.Settings)
}

func TestPersonFollowConfigReceiptsAndOwnerPG(t *testing.T) {
	f := newPersonFollowFixture(t)
	id := uuid.New()
	in := f.input()
	in.Settings.Enabled = false
	in.Settings.Bindings = f.bindings(t)
	r := f.save(t, id, in, "create")
	before := f.counts(t)
	if !reflect.DeepEqual(before, []int64{1, 1, 1, 1, 1, 0, 0, 0, 0, 0}) {
		t.Fatal("save created work or incomplete ledger", before)
	}
	in.Settings.Bindings.IndexerIDs[0], in.Settings.Bindings.IndexerIDs[1] = in.Settings.Bindings.IndexerIDs[1], in.Settings.Bindings.IndexerIDs[0]
	replay := f.save(t, id, in, "create")
	if !samePersonFollow(r.Config, replay.Config) {
		t.Fatal("binding permutation changed original receipt")
	}
	next := f.input()
	next.Settings.Enabled = false
	next.ExpectedVersion = 1
	kept := f.save(t, id, next, "keep")
	if !reflect.DeepEqual(kept.Config.Settings.Bindings, r.Config.Settings.Bindings) {
		t.Fatal("omission cleared bindings")
	}
	next.ExpectedVersion = 2
	next.Settings.Bindings = &domain.Bindings{IndexerIDs: []uuid.UUID{}}
	cleared := f.save(t, id, next, "clear")
	if cleared.Config.Settings.Bindings == nil || len(cleared.Config.Settings.Bindings.IndexerIDs) != 0 {
		t.Fatal("explicit clearing lost")
	}
	before = f.counts(t)
	if receipt, err := f.service.Receipt(t.Context(), f.actor, "create"); err != nil || !samePersonFollow(receipt.Config, r.Config) {
		t.Fatal("historical receipt changed", err)
	}
	if old := f.save(t, id, in, "create"); !samePersonFollow(old.Config, r.Config) {
		t.Fatal("original save selected a newer configuration")
	}
	if _, err := f.service.Read(t.Context(), f.actor, id); err != nil {
		t.Fatal(err)
	}
	rows, err := f.service.List(t.Context(), f.actor)
	if err != nil || len(rows) != 1 || rows[0].Version != 3 {
		t.Fatal("current read failed", err)
	}
	other := pgtest.CreateUserActor(t, db.New(f.p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	if _, err = f.service.Receipt(t.Context(), other, "create"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("cross-account receipt", err)
	}
	if _, err = f.service.Read(t.Context(), other, id); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("cross-account configuration", err)
	}
	otherRows, err := f.service.List(t.Context(), other)
	if err != nil || len(otherRows) != 0 {
		t.Fatal("cross-account listing", err)
	}
	if !reflect.DeepEqual(before, f.counts(t)) {
		t.Fatal("read/replay wrote business tables")
	}
	f.exec(t, `UPDATE library_root SET health='offline' WHERE id=$1`, f.root)
	if old := f.save(t, id, in, "create"); !samePersonFollow(old.Config, r.Config) {
		t.Fatal("offline source prevented old-key recovery")
	}
}

func TestPersonFollowConfigConcurrentAndQuotaPG(t *testing.T) {
	t.Run("same_key_and_versions", func(t *testing.T) {
		f := newPersonFollowFixture(t)
		id := uuid.New()
		in := f.input()
		var wg sync.WaitGroup
		results := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := f.service.Save(t.Context(), f.actor, id, in, "same-key")
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := f.counts(t); !reflect.DeepEqual(got, []int64{1, 1, 1, 1, 1, 0, 0, 0, 0, 0}) {
			t.Fatal("duplicate save ledger", got)
		}
		results = make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				input := f.input()
				input.ExpectedVersion = 1
				input.Settings.IntervalMinutes = 30 + n
				_, err := f.service.Save(t.Context(), f.actor, id, input, fmt.Sprintf("version-%d", n))
				results <- err
			}(i)
		}
		wg.Wait()
		close(results)
		wins, conflicts := 0, 0
		for err := range results {
			if err == nil {
				wins++
			} else if errors.Is(err, ports.ErrPersonFollowConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if wins != 1 || conflicts != 1 {
			t.Fatal("CAS did not choose one winner", wins, conflicts)
		}
		if _, err := f.service.Save(t.Context(), f.actor, uuid.New(), f.input(), "duplicate-person"); !errors.Is(err, ports.ErrPersonFollowConflict) {
			t.Fatal("duplicate person/role admitted", err)
		}
	})
	t.Run("quota_and_full_list", func(t *testing.T) {
		f := newPersonFollowFixture(t)
		for i := 0; i < 100; i++ {
			in := f.input()
			in.Settings.PersonID += int64(i)
			f.save(t, uuid.New(), in, fmt.Sprintf("follow-%d", i))
		}
		rows, err := f.service.List(t.Context(), f.actor)
		if err != nil || len(rows) != 100 {
			t.Fatal("quota list incomplete", err)
		}
		before := f.counts(t)
		in := f.input()
		in.Settings.PersonID = 99999
		if _, err = f.service.Save(t.Context(), f.actor, uuid.New(), in, "over-quota"); !errors.Is(err, ports.ErrPersonFollowConflict) {
			t.Fatal("quota bypass", err)
		}
		if !reflect.DeepEqual(before, f.counts(t)) {
			t.Fatal("quota failure changed ledger")
		}
	})
}

func TestPersonFollowConfigTargetsAndIdentityPG(t *testing.T) {
	f := newPersonFollowFixture(t)
	id := uuid.New()
	original := f.save(t, id, f.input(), "original")
	before := f.counts(t)
	for _, role := range []string{"actor", "director"} {
		in := f.input()
		in.ExpectedVersion = 1
		in.Settings.Role = role
		if role == "director" {
			in.Settings.PersonID++
		}
		if _, err := f.service.Save(t.Context(), f.actor, id, in, "identity-"+role); !errors.Is(err, ports.ErrPersonFollowConflict) {
			t.Fatal("identity rewritten", err)
		}
	}
	in := f.input()
	in.ExpectedVersion = 1
	in.Settings.QualityPolicyRevision = 2
	if _, err := f.service.Save(t.Context(), f.actor, id, in, "policy"); !errors.Is(err, ports.ErrPersonFollowConflict) {
		t.Fatal("unpublished policy accepted", err)
	}
	f.exec(t, `UPDATE library_root SET health='offline' WHERE id=$1`, f.root)
	in = f.input()
	in.ExpectedVersion = 1
	if _, err := f.service.Save(t.Context(), f.actor, id, in, "offline"); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("offline target accepted", err)
	}
	in.Settings.Enabled = false
	disabled := f.save(t, id, in, "disable-offline")
	if disabled.Config.Settings.Enabled || disabled.Config.Version != 2 {
		t.Fatal("offline disable failed")
	}
	old, err := f.service.Receipt(t.Context(), f.actor, "original")
	if err != nil || !samePersonFollow(old.Config, original.Config) {
		t.Fatal("old receipt depends on target health", err)
	}
	if got := f.counts(t); got[0] != before[0] || got[1] != before[1]+1 || got[5] != 0 || got[6] != 0 {
		t.Fatal("failed target validation wrote state", got)
	}
	tv := uuid.New()
	f.exec(t, `INSERT INTO library_root(id,path,media_type,name,health)VALUES($1,$2,'tv','Wrong root','online')`, tv, t.TempDir())
	in = f.input()
	in.ExpectedVersion = 2
	in.Settings.MovieRootID = &tv
	if _, err = f.service.Save(t.Context(), f.actor, id, in, "wrong-kind"); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("root kind mismatch accepted", err)
	}
}

func TestPersonFollowConfigPermissionAndCancelledLockPG(t *testing.T) {
	f := newPersonFollowFixture(t)
	id := uuid.New()
	f.save(t, id, f.input(), "original")
	before := f.counts(t)
	hold, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err = hold.Exec(t.Context(), `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	in := f.input()
	in.ExpectedVersion = 1
	_, err = f.service.Save(ctx, f.actor, id, in, "cancelled")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock ignored cancellation", err)
	}
	if _, err = hold.Exec(t.Context(), `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	if err = hold.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Save(t.Context(), f.actor, id, in, "revoked"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revoked save", err)
	}
	if _, err = f.service.Receipt(t.Context(), f.actor, "original"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revoked receipt", err)
	}
	if !reflect.DeepEqual(before, f.counts(t)) {
		t.Fatal("cancelled or revoked command wrote state")
	}
	f.exec(t, `UPDATE app_user SET is_active=true WHERE id=$1`, f.actor.ID)
	f.save(t, id, in, "cancelled")
}

func TestPersonFollowConfigTailRollbackPG(t *testing.T) {
	for _, table := range []string{"person_follow_config_history", "outbox_event", "audit_log", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := newPersonFollowFixture(t)
			id := uuid.New()
			in := f.input()
			before := f.counts(t)
			f.exec(t, `CREATE FUNCTION person_follow_tail_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned private SQL'; END $$`)
			f.exec(t, "CREATE TRIGGER person_follow_tail_fault BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION person_follow_tail_fault()")
			if _, err := f.service.Save(t.Context(), f.actor, id, in, "create"); !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe tail failure", err)
			}
			if !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("creation partially committed")
			}
			f.exec(t, "DROP TRIGGER person_follow_tail_fault ON "+table)
			old := f.save(t, id, in, "create")
			before = f.counts(t)
			f.exec(t, "CREATE TRIGGER person_follow_tail_fault BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION person_follow_tail_fault()")
			in.ExpectedVersion = 1
			in.Settings.IntervalMinutes = 30
			if _, err := f.service.Save(t.Context(), f.actor, id, in, "update"); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("update tail fault ignored", err)
			}
			current, err := f.service.Read(t.Context(), f.actor, id)
			if err != nil || !samePersonFollow(*current, old.Config) || !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("update partially committed", err)
			}
			f.exec(t, "DROP TRIGGER person_follow_tail_fault ON "+table)
			f.save(t, id, in, "update")
		})
	}
}

func TestPersonFollowConfigMissingReceiptFencePG(t *testing.T) {
	for _, corruption := range []string{"missing", "inconsistent"} {
		t.Run(corruption, func(t *testing.T) {
			f := newPersonFollowFixture(t)
			id := uuid.New()
			in := f.input()
			f.save(t, id, in, "original")
			if corruption == "missing" {
				f.exec(t, `DELETE FROM idempotency_record WHERE idempotency_key='person.follow.config:original'`)
			} else {
				f.exec(t, `UPDATE idempotency_record SET response_body='{}'::jsonb WHERE idempotency_key='person.follow.config:original'`)
			}
			before := f.counts(t)
			if _, err := f.service.Save(t.Context(), f.actor, id, in, "original"); !errors.Is(err, ports.ErrPersonFollowConflict) {
				t.Fatal("missing original ledger recreated", err)
			}
			if _, err := f.service.Receipt(t.Context(), f.actor, "original"); !errors.Is(err, ports.ErrPersonFollowConflict) {
				t.Fatal("inconsistent receipt accepted", err)
			}
			if !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("corrupt original receipt duplicated writes")
			}
		})
	}
}

func TestPersonFollowConfigSchema122HistoryPG(t *testing.T) {
	f := newPersonFollowFixture(t)
	if version, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history); err != nil || version != 121 {
		t.Fatal("empty rollback", version, err)
	}
	if _, err := f.service.List(t.Context(), f.actor); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("missing contract became empty success", err)
	}
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), f.history); err != nil || version != 122 {
		t.Fatal(version, err)
	}
	id := uuid.New()
	f.save(t, id, f.input(), "original")
	before := f.counts(t)
	for _, query := range []string{`UPDATE person_follow_config SET person_id=person_id+1,version=version+1,updated_at=clock_timestamp()`, `DELETE FROM person_follow_config`, `TRUNCATE person_follow_config CASCADE`, `UPDATE person_follow_config_history SET version=version+1`, `DELETE FROM person_follow_config_history`, `TRUNCATE person_follow_config_history`} {
		if _, err := f.p.Exec(t.Context(), query); err == nil {
			t.Fatal("immutable history changed")
		}
	}
	if _, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history); err == nil {
		t.Fatal("rollback discarded follow history")
	}
	if !reflect.DeepEqual(before, f.counts(t)) {
		t.Fatal("history protection changed data")
	}
	if _, err := f.service.Receipt(t.Context(), f.actor, "original"); err != nil {
		t.Fatal("history rejection broke receipt", err)
	}
}
