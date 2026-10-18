package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"testing"
	"time"
)

type frozenUpgradeFixture struct {
	p                                                      *pgxpool.Pool
	store                                                  *postgres.UpgradeReservationStore
	actor                                                  *ports.Actor
	sub, unit, root, file, fact, candidate, client, source uuid.UUID
	dsn                                                    string
}

func frozenUpgrade(t *testing.T) *frozenUpgradeFixture {
	t.Helper()
	ctx := context.Background()
	f := &frozenUpgradeFixture{dsn: pgtest.GetCluster(t).NewTestDB(t)}
	if v, e := postgres.Migrate(ctx, f.dsn, frozenMigrationDir(t)); e != nil || v != 64 {
		t.Fatalf("migrate64: %d %v", v, e)
	}
	p, e := pgxpool.New(ctx, f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	f.p = p
	t.Cleanup(p.Close)
	var version string
	if e = p.QueryRow(ctx, "SHOW server_version_num").Scan(&version); e != nil || version != "180006" {
		t.Fatalf("requires pinned native PG18.6: %q %v", version, e)
	}
	q := db.New(p)
	gen := idgen.New()
	f.store = postgres.NewUpgradeReservationStore(p, gen)
	actor := uuid.New()
	f.actor = &ports.Actor{ID: actor.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	if _, e = q.CreateUser(ctx, db.CreateUserParams{ID: actor, Username: "admin", PasswordHash: "unused", Role: "admin", IsActive: true}); e != nil {
		t.Fatal(e)
	}
	f.root = uuid.New()
	if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: f.root, Path: "/never-open-frozen-upgrade", Name: "Movies", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1", f.root)
	target := uuid.New()
	if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: target, Title: "Fight Club", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE media SET year=1999 WHERE id=$1", target)
	f.unit = uuid.New()
	if _, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: f.unit, MediaID: target, UnitKey: "movie"}); e != nil {
		t.Fatal(e)
	}
	f.source = uuid.New()
	if _, e = q.CreateIndexer(ctx, db.CreateIndexerParams{ID: f.source, Name: "No network", Url: "http://127.0.0.1:1", ApiKeyEncrypted: []byte("not-a-credential"), Categories: []byte("[]")}); e != nil {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE indexer SET health='online' WHERE id=$1", f.source)
	f.candidate = uuid.New()
	title := "Fight.Club.1999.2160p.Bluray.x265"
	profile, _ := json.Marshal(media.Recognize(title).Quality)
	if _, e = q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: f.candidate, IndexerID: f.source, Guid: "frozen-upgrade", Title: title, LinkEncrypted: []byte("never-decrypt"), SizeBytes: 1000, QualityProfile: profile, SourceConfigVersion: 1}); e != nil {
		t.Fatal(e)
	}
	query := uuid.New()
	f.exec(t, "INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,finished_at) VALUES($1,$2,'search','Fight Club',1,'succeeded',clock_timestamp())", query, f.source)
	f.exec(t, "INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)", query, f.candidate)
	rules, _ := json.Marshal(quality.PolicyRules{CutoffScore: 1000, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300, quality.Res2160p: 400}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40}})
	if _, e = q.CreatePolicy(ctx, db.CreatePolicyParams{ID: "frozen-upgrade", Name: "Frozen upgrade"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.InsertPolicyRevision(ctx, db.InsertPolicyRevisionParams{ID: uuid.New(), PolicyID: "frozen-upgrade", Revision: 1, Rules: rules}); e != nil {
		t.Fatal(e)
	}
	f.client = uuid.New()
	if _, e = q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: f.client, Name: "Never called", ClientType: "qbittorrent", Url: "http://127.0.0.1:1", PasswordEncrypted: []byte("never-decrypt"), RemotePathMap: []byte("{}")}); e != nil {
		t.Fatal(e)
	}
	sources, _ := json.Marshal([]uuid.UUID{f.source})
	f.sub = uuid.New()
	if _, e = q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: f.sub, UserID: actor, MediaID: target, RootID: f.root, QualityPolicyID: "frozen-upgrade", QualityPolicyRevision: 1, IndexerIds: sources, ScopeType: "all"}); e != nil {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE subscription SET downloader_id=$2 WHERE id=$1", f.sub, f.client)
	f.file = uuid.New()
	f.exec(t, "INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'original.mkv',100,1)", f.file, f.root)
	if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: f.file, UnitID: f.unit}); e != nil {
		t.Fatal(e)
	}
	f.fact = uuid.New()
	old, _ := json.Marshal(media.Recognize("Fight.Club.1999.1080p.Bluray.x264").Quality)
	if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: f.fact, UnitID: f.unit, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: f.file, Valid: true}, RootID: pgtype.UUID{Bytes: f.root, Valid: true}, QualityProfile: old}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.UpsertWantedItem(ctx, db.UpsertWantedItemParams{ID: uuid.New(), SubscriptionID: f.sub, UnitID: f.unit, IntentVersion: 1}); e != nil {
		t.Fatal(e)
	}
	f.project(t)
	return f
}
func (f *frozenUpgradeFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, e := f.p.Exec(context.Background(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func (f *frozenUpgradeFixture) project(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	rows, e := db.New(f.p).ListWantedItemsForSubscription(ctx, f.sub)
	if e != nil {
		t.Fatal(e)
	}
	for _, w := range rows {
		if _, e = subscription.NewEvaluator(postgres.NewWantedProjectionStore(f.p)).EvaluateWantedItem(ctx, w.ID, quality.Policy{ID: "frozen-upgrade", Revision: 1}); e != nil {
			t.Fatalf("project upgrade fixture: %v", e)
		}
	}
}
func (f *frozenUpgradeFixture) enable(t *testing.T) {
	t.Helper()
	var intent int64
	if e := f.p.QueryRow(context.Background(), "SELECT intent_version FROM subscription WHERE id=$1", f.sub).Scan(&intent); e != nil {
		t.Fatal(e)
	}
	if _, e := f.store.SetUpgradeMode(context.Background(), f.actor, ports.SetUpgradeModeCommand{SubscriptionID: f.sub, ExpectedIntent: intent, Mode: ports.UpgradeStopAtCutoff, Confirmed: true}, "enable"); e != nil {
		t.Fatalf("enable upgrade fixture: %v", e)
	}
	f.project(t)
}
func (f *frozenUpgradeFixture) command(t *testing.T) ports.ReserveUpgradeCommand {
	t.Helper()
	v, e := postgres.NewUpgradePreviewStore(f.p).ReadUpgradePreview(context.Background(), f.actor, ports.UpgradePreviewRequest{SubscriptionID: f.sub, CurrentFileID: f.file, CandidateID: f.candidate})
	if e != nil || v.HoldReason != "" {
		if v != nil {
			t.Fatalf("preview: hold=%s err=%v", v.HoldReason, e)
		}
		t.Fatalf("preview: %v", e)
	}
	return ports.ReserveUpgradeCommand{SubscriptionID: f.sub, CurrentFileID: f.file, CandidateID: f.candidate, Expected: v.Version, ExpectedClientDigest: frozenClientDigest(t, f), Confirmed: true}
}
func (f *frozenUpgradeFixture) oldState(t *testing.T) string {
	t.Helper()
	var out string
	e := f.p.QueryRow(context.Background(), `SELECT jsonb_build_array((SELECT to_jsonb(x) FROM library_file x WHERE id=$1),(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM library_file_unit x WHERE file_id=$1),(SELECT to_jsonb(x) FROM unit_fact x WHERE id=$2))::text`, f.file, f.fact).Scan(&out)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *frozenUpgradeFixture) assertNoReservation(t *testing.T) {
	t.Helper()
	var n int
	if e := f.p.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM movie_upgrade_authority)+(SELECT count(*) FROM acquisition_reservation)+(SELECT count(*) FROM unit_reservation)+(SELECT count(*) FROM download_requirement)+(SELECT count(*) FROM audit_log WHERE action='upgrade.reserve')+(SELECT count(*) FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.reserve:%')").Scan(&n); e != nil || n != 0 {
		t.Fatalf("rollback leaked %d: %v", n, e)
	}
}

func TestFrozenUpgradeModePG(t *testing.T) {
	f := frozenUpgrade(t)
	ctx := context.Background()
	var mode string
	var intent int64
	if e := f.p.QueryRow(ctx, "SELECT upgrade_mode,intent_version FROM subscription WHERE id=$1", f.sub).Scan(&mode, &intent); e != nil || mode != "disabled" {
		t.Fatal(mode, e)
	}
	c := f.command(t)
	if _, e := f.store.ReserveUpgrade(ctx, f.actor, c, "off"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	f.assertNoReservation(t)
	m := ports.SetUpgradeModeCommand{SubscriptionID: f.sub, ExpectedIntent: intent, Mode: ports.UpgradeMode("unsupported"), Confirmed: true}
	if _, e := f.store.SetUpgradeMode(ctx, f.actor, m, "unsupported"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	m.Mode = ports.UpgradeStopAtCutoff
	r, e := f.store.SetUpgradeMode(ctx, f.actor, m, "mode")
	if e != nil || r.IntentVersion != intent+1 {
		t.Fatal(r, e)
	}
	r2, e := f.store.SetUpgradeMode(ctx, f.actor, m, "mode")
	if e != nil || *r2 != *r {
		t.Fatal(r2, e)
	}
	if _, e = f.store.SetUpgradeMode(ctx, f.actor, m, "mode-new"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	var current bool
	if e = f.p.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=(SELECT id FROM wanted_item WHERE subscription_id=$1)", f.sub).Scan(&current); e != nil || current {
		t.Fatal("stale Wanted accepted", e)
	}
	if _, e = f.store.ReserveUpgrade(ctx, f.actor, c, "stale"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
}
func TestFrozenUpgradeConcurrentReceiptPG(t *testing.T) {
	f := frozenUpgrade(t)
	f.enable(t)
	c := f.command(t)
	before := f.oldState(t)
	var wg sync.WaitGroup
	results := make([]*ports.UpgradeReservationResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = f.store.ReserveUpgrade(context.Background(), f.actor, c, "same")
		}()
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || *results[0] != *results[1] {
		t.Fatalf("same-key: %+v %+v", results, errs)
	}
	r := results[0]
	if r.Status != "reserved_not_executable" || r.ExecutionAllowed {
		t.Fatal(r)
	}
	var a, l, u, d, j, n int
	if e := f.p.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_authority),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM job),(SELECT count(*) FROM download_task)`).Scan(&a, &l, &u, &d, &j, &n); e != nil || a != 1 || l != 1 || u != 1 || d != 1 || j != 0 || n != 0 {
		t.Fatalf("counts %d %d %d %d %d %d %v", a, l, u, d, j, n, e)
	}
	if before != f.oldState(t) {
		t.Fatal("old file/fact/history changed")
	}
	if _, e := f.store.ReserveUpgrade(context.Background(), f.actor, c, "new-key"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	changed := c
	changed.Confirmed = false
	if _, e := f.store.ReserveUpgrade(context.Background(), f.actor, changed, "same"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE subscription SET upgrade_mode='disabled' WHERE id=$1", f.sub)
	got, e := f.store.ReserveUpgrade(context.Background(), f.actor, c, "same")
	if e != nil || *got != *r {
		t.Fatal("historical receipt", got, e)
	}
	f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID))
	if _, e = f.store.ReserveUpgrade(context.Background(), f.actor, c, "same"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("receipt bypassed revoked account", e)
	}
	if _, e = f.p.Exec(context.Background(), "UPDATE movie_upgrade_authority SET snapshot=snapshot WHERE operation_id=$1", r.OperationID); e == nil {
		t.Fatal("mutable history")
	}
	if _, e = postgres.Rollback(context.Background(), f.dsn, frozenMigrationDir(t)); e == nil {
		t.Fatal("occupied downgrade succeeded")
	}
}
func TestFrozenUpgradeLateFaultPG(t *testing.T) {
	for _, stage := range []string{"audit_log", "outbox_event", "idempotency_record", "download_requirement"} {
		t.Run(stage, func(t *testing.T) {
			f := frozenUpgrade(t)
			f.enable(t)
			c := f.command(t)
			before := f.oldState(t)
			f.exec(t, fmt.Sprintf(`CREATE FUNCTION test_upgrade_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE library_file SET size_bytes=size_bytes+1 WHERE id='%s'; RETURN NEW; END $$; CREATE TRIGGER test_upgrade_tail AFTER INSERT ON %s FOR EACH ROW EXECUTE FUNCTION test_upgrade_tail()`, f.file, stage))
			if _, e := f.store.ReserveUpgrade(context.Background(), f.actor, c, "late"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
				t.Fatal("late mutation accepted", e)
			}
			f.assertNoReservation(t)
			if before != f.oldState(t) {
				t.Fatal("rollback failed to restore old file/fact")
			}
		})
	}
}
func TestFrozenUpgradeSharedFilePG(t *testing.T) {
	f := frozenUpgrade(t)
	f.enable(t)
	c := f.command(t)
	var mediaID uuid.UUID
	if e := f.p.QueryRow(context.Background(), "SELECT media_id FROM media_unit WHERE id=$1", f.unit).Scan(&mediaID); e != nil {
		t.Fatal(e)
	}
	other := uuid.New()
	f.exec(t, "INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'other')", other, mediaID)
	f.exec(t, "INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", f.file, other)
	if _, e := f.store.ReserveUpgrade(context.Background(), f.actor, c, "shared"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	f.assertNoReservation(t)
}
func TestFrozenUpgradeLockWaitRevocationPG(t *testing.T) {
	f := frozenUpgrade(t)
	f.enable(t)
	c := f.command(t)
	ctx := context.Background()
	tx, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, tx)
	if _, e = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID)); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.store.ReserveUpgrade(ctx, f.actor, c, "revoked"); done <- e }()
	select {
	case e := <-done:
		t.Fatalf("account lock not respected %v", e)
	case <-time.After(80 * time.Millisecond):
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if !errors.Is(e, ports.ErrForbidden) {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reservation stuck")
	}
	f.assertNoReservation(t)
}
func TestFrozenUpgradeCandidateWaitDeadlinePG(t *testing.T) {
	f := frozenUpgrade(t)
	f.enable(t)
	c := f.command(t)
	ctx := context.Background()
	tx, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, tx)
	if _, e = tx.Exec(ctx, "SELECT id FROM release_candidate WHERE id=$1 FOR UPDATE", f.candidate); e != nil {
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, e = f.store.ReserveUpgrade(deadline, f.actor, c, "deadline"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	f.assertNoReservation(t)
}
func TestFrozenUpgradeOwnFenceTamperPG(t *testing.T) {
	f := frozenUpgrade(t)
	f.enable(t)
	c := f.command(t)
	f.exec(t, `CREATE FUNCTION test_upgrade_fence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.submission_fence='{}'::jsonb; RETURN NEW; END $$; CREATE TRIGGER test_upgrade_fence BEFORE INSERT ON download_requirement FOR EACH ROW EXECUTE FUNCTION test_upgrade_fence()`)
	if _, e := f.store.ReserveUpgrade(context.Background(), f.actor, c, "fence"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	f.assertNoReservation(t)
}
func TestFrozenUpgradeEmptyDowngradePG(t *testing.T) {
	f := frozenUpgrade(t)
	if v, e := postgres.Rollback(context.Background(), f.dsn, frozenMigrationDir(t)); e != nil || v != 63 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Migrate(context.Background(), f.dsn, frozenMigrationDir(t)); e != nil || v != 64 {
		t.Fatal(v, e)
	}
}

// Keep pgx imported here as a compile-time assertion of the direct transaction
// fixture, rather than accidentally switching this suite back to App.New.
var _ pgx.Tx
