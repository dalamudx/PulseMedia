package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type overviewFixture struct {
	pool  *pgxpool.Pool
	dsn   string
	actor *ports.Actor
	root  uuid.UUID
	store *postgres.LibraryOverviewStore
}

func newOverviewFixture(t *testing.T, through ...int64) overviewFixture {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, repositoryMigrationsThrough(t, through...)); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := overviewFixture{pool: pool, dsn: dsn, root: uuid.New(), actor: &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}}
	f.store = postgres.NewLibraryOverviewStore(pool)
	var version int
	if err = pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&version); err != nil || version != 180006 {
		t.Fatalf("requires PG18.6: %d %v", version, err)
	}
	f.exec(t, "INSERT INTO app_user(id,username,password_hash,role,is_active) VALUES($1,'overview-member','fixture','member',true)", uuid.MustParse(f.actor.ID))
	f.exec(t, "INSERT INTO library_root(id,path,name,media_type) VALUES($1,$2,'Shared','tv')", f.root, filepath.Join(t.TempDir(), "shared"))
	return f
}

func (f overviewFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f overviewFixture) read(t *testing.T, limit int) *ports.LibraryOverview {
	t.Helper()
	out, err := f.store.ReadLibraryOverview(context.Background(), f.actor, ports.LibraryOverviewRequest{RecentLimit: limit})
	if err != nil || out == nil {
		t.Fatalf("overview: %+v %v", out, err)
	}
	return out
}
func (f overviewFixture) media(t *testing.T, kind string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	m, u := uuid.New(), uuid.New()
	f.exec(t, "INSERT INTO media(id,title,media_type,year) VALUES($1,$2,$3,2026)", m, "Work "+m.String(), kind)
	key := "S00E01"
	if kind == "movie" {
		key = "movie"
	}
	f.exec(t, "INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,$3)", u, m, key)
	return m, u
}
func (f overviewFixture) file(t *testing.T, root, unit uuid.UUID, stage string, at time.Time) uuid.UUID {
	t.Helper()
	file := uuid.New()
	f.exec(t, "INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,$3,42,1)", file, root, file.String()+".mkv")
	f.exec(t, "INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", file, unit)
	f.fact(t, file, root, unit, stage, at)
	return file
}
func (f overviewFixture) fact(t *testing.T, file, root, unit uuid.UUID, stage string, at time.Time) {
	t.Helper()
	f.exec(t, "INSERT INTO unit_fact(id,source_file_id,root_id,unit_id,stage,created_at) VALUES($1,$2,$3,$4,$5,$6)", uuid.New(), file, root, unit, stage, at)
}
func (f overviewFixture) now(t *testing.T) time.Time {
	t.Helper()
	var now time.Time
	if err := f.pool.QueryRow(context.Background(), "SELECT transaction_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}
func (f overviewFixture) scan(t *testing.T, root uuid.UUID, at time.Time, inode int64) uuid.UUID {
	t.Helper()
	run := uuid.New()
	f.exec(t, "INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,created_at,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',$3,$3 FROM library_root WHERE id=$2", run, root, at)
	f.exec(t, "INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,$3,true,$4 FROM library_root WHERE id=$2", run, root, inode, at)
	f.exec(t, "UPDATE library_root SET baseline_completed=true WHERE id=$1", root)
	f.exec(t, "INSERT INTO root_capacity_observation(scan_run_id,total_bytes,available_bytes,sampled_at) VALUES($1,9007199254740993,4007199254740993,$2)", run, at)
	return run
}

func TestLibraryOverviewCountsAndRecent(t *testing.T) {
	f := newOverviewFixture(t)
	now := f.now(t)
	// More than the catalog page size: counts must not come from a paginated list.
	for i := 0; i < 130; i++ {
		_, u := f.media(t, "movie")
		f.file(t, f.root, u, "imported", now.Add(-time.Duration(i+1)*time.Hour))
	}
	show, u := f.media(t, "tv")
	multi := f.file(t, f.root, u, "imported", now.Add(-time.Minute))
	f.fact(t, multi, f.root, u, "imported", now)
	second := uuid.New()
	f.exec(t, "INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'S01E02')", second, show)
	f.exec(t, "INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", multi, second)
	f.fact(t, multi, f.root, second, "imported", now)
	otherRoot := uuid.New()
	f.exec(t, "INSERT INTO library_root(id,path,name,media_type,health) VALUES($1,$2,'Offline shared','tv','offline')", otherRoot, filepath.Join(t.TempDir(), "offline"))
	f.file(t, otherRoot, u, "imported", now.Add(-2*time.Minute))
	f.file(t, f.root, u, "imported", now.Add(-3*time.Minute))
	for _, mode := range []string{"revoked", "expired", "file_retired", "mapping_retired", "unit_retired", "confirmed"} {
		_, unit := f.media(t, "movie")
		stage := "imported"
		if mode == "confirmed" {
			stage = "confirmed"
		}
		file := f.file(t, f.root, unit, stage, now)
		switch mode {
		case "revoked":
			f.exec(t, "UPDATE unit_fact SET revoked_at=transaction_timestamp() WHERE source_file_id=$1", file)
		case "expired":
			f.exec(t, "UPDATE unit_fact SET expires_at=transaction_timestamp()-interval '1 second' WHERE source_file_id=$1", file)
			f.fact(t, file, f.root, unit, "confirmed", now)
		case "file_retired":
			f.exec(t, "UPDATE library_file SET retired_at=transaction_timestamp() WHERE id=$1", file)
		case "mapping_retired":
			f.exec(t, "UPDATE library_file_unit SET retired_at=transaction_timestamp() WHERE file_id=$1", file)
		case "unit_retired":
			f.exec(t, "UPDATE media_unit SET retired=true WHERE id=$1", unit)
		}
	}
	out := f.read(t, 0)
	want := ports.LibraryOverviewCounts{Movies: "130", TVShows: "1", Episodes: "2", Files: "133"}
	if out.Counts != want || len(out.Recent) != 12 || out.Recent[0].MediaID != show {
		t.Fatalf("counts/recent: %+v %+v", out.Counts, out.Recent)
	}
	if !out.Recent[0].ImportedAt.Equal(now.Add(-time.Minute)) {
		t.Fatal("duplicate/multi-unit fact reset arrival", out.Recent[0])
	}
	if len(f.read(t, 24).Recent) != 24 {
		t.Fatal("bounded recent limit")
	}
	if len(out.Roots) != 2 || out.Roots[0].Name != "Offline shared" || out.Roots[0].Availability != "unknown" {
		t.Fatal("root order/offline", out.Roots)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "/data/") || strings.Contains(string(raw), "marker") {
		t.Fatal("root configuration leaked", string(raw))
	}
	// A mapping correction inherits this file's earliest imported history, including revoked facts.
	oldMedia, oldUnit := f.media(t, "movie")
	corrected := f.file(t, f.root, oldUnit, "imported", now.Add(-5*time.Hour))
	f.exec(t, "UPDATE unit_fact SET revoked_at=transaction_timestamp() WHERE source_file_id=$1", corrected)
	f.exec(t, "UPDATE library_file_unit SET retired_at=transaction_timestamp() WHERE file_id=$1", corrected)
	newMedia, newUnit := f.media(t, "movie")
	f.exec(t, "INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", corrected, newUnit)
	f.fact(t, corrected, f.root, newUnit, "imported", now)
	_, betweenUnit := f.media(t, "movie")
	betweenMedia := uuid.Nil
	if err = f.pool.QueryRow(context.Background(), "SELECT media_id FROM media_unit WHERE id=$1", betweenUnit).Scan(&betweenMedia); err != nil {
		t.Fatal(err)
	}
	f.file(t, f.root, betweenUnit, "imported", now.Add(-30*time.Minute))
	recent := f.read(t, 24).Recent
	foundBetween, foundCorrected := false, false
	for _, item := range recent {
		if item.MediaID == oldMedia {
			t.Fatal("retired historical mapping visible")
		}
		if item.MediaID == betweenMedia {
			foundBetween = true
		}
		if item.MediaID == newMedia {
			foundCorrected = true
			if !foundBetween || !item.ImportedAt.Equal(now.Add(-5*time.Hour)) {
				t.Fatal("mapping correction reordered arrival", item)
			}
		}
	}
	if !foundCorrected {
		t.Fatal("corrected media missing from recent")
	}
	// Equal timestamp ordering is UUID descending, independent of insertion order.
	a, au := f.media(t, "movie")
	b, bu := f.media(t, "movie")
	f.file(t, f.root, au, "imported", now)
	f.file(t, f.root, bu, "imported", now)
	recent = f.read(t, 1).Recent
	if a.String() < b.String() {
		a = b
	}
	if len(recent) != 1 || recent[0].MediaID != a {
		t.Fatal("timestamp UUID tie", recent)
	}
}

func TestLibraryOverviewCapacity(t *testing.T) {
	for _, mode := range []string{"fresh", "stale", "future", "offline", "baseline_pending", "generation", "path", "marker", "type", "identity", "unsealed", "missing_capacity", "failed", "partial", "running", "invalid", "new_generation", "nonfinite", "missing_finalization", "token"} {
		t.Run(mode, func(t *testing.T) {
			f := newOverviewFixture(t)
			now := f.now(t)
			f.scan(t, f.root, now.Add(-time.Hour), 99)
			latest := f.scan(t, f.root, now.Add(-time.Minute), 99)
			state, availability := "unknown", "unknown"
			switch mode {
			case "fresh":
				state, availability = "fresh", "available"
				// Baseline publication advances root health without changing trusted configuration.
				f.exec(t, "UPDATE scan_finalization SET expected_health_revision=1 WHERE scan_run_id=$1", latest)
			case "stale":
				state, availability = "stale", "available"
				f.exec(t, "UPDATE root_capacity_observation SET sampled_at=transaction_timestamp()-interval '31 minutes' WHERE scan_run_id=$1", latest)
			case "future":
				availability = "available"
				f.exec(t, "UPDATE root_capacity_observation SET sampled_at=transaction_timestamp()+interval '1 minute' WHERE scan_run_id=$1", latest)
			case "offline":
				f.exec(t, "UPDATE library_root SET health='offline' WHERE id=$1", f.root)
			case "baseline_pending":
				f.exec(t, "UPDATE library_root SET baseline_completed=false WHERE id=$1", f.root)
			case "generation":
				f.exec(t, "UPDATE library_root SET generation=generation+1 WHERE id=$1", f.root)
			case "path":
				f.exec(t, "UPDATE scan_finalization SET expected_path=expected_path||'-changed' WHERE scan_run_id=$1", latest)
			case "marker":
				f.exec(t, "UPDATE scan_finalization SET expected_marker_filename='other-marker' WHERE scan_run_id=$1", latest)
			case "type":
				f.exec(t, "UPDATE scan_finalization SET expected_media_type='movie' WHERE scan_run_id=$1", latest)
			case "identity":
				f.exec(t, "UPDATE scan_finalization SET root_inode=100 WHERE scan_run_id=$1", latest)
			case "unsealed":
				f.exec(t, "UPDATE scan_finalization SET completed_at=NULL WHERE scan_run_id=$1", latest)
			case "missing_capacity":
				availability = "available"
				f.exec(t, "DELETE FROM root_capacity_observation WHERE scan_run_id=$1", latest)
			case "new_generation":
				state, availability = "fresh", "available"
				f.exec(t, "UPDATE library_root SET generation=generation+1 WHERE id=$1", f.root)
				f.scan(t, f.root, now.Add(-time.Second), 100)
			case "nonfinite":
				availability = "available"
				f.exec(t, "UPDATE root_capacity_observation SET sampled_at='-infinity'::timestamptz WHERE scan_run_id=$1", latest)
			case "missing_finalization":
				f.exec(t, "DELETE FROM scan_finalization WHERE scan_run_id=$1", latest)
			case "token":
				f.exec(t, "UPDATE library_root SET scan_token = 1 WHERE id=$1", f.root)
			case "invalid":
				availability = "available"
				f.exec(t, "ALTER TABLE root_capacity_observation DROP CONSTRAINT root_capacity_observation_check")
				f.exec(t, "UPDATE root_capacity_observation SET available_bytes=total_bytes+1 WHERE scan_run_id=$1", latest)
			default:
				f.exec(t, "INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,created_at) SELECT $1,id,scan_token,generation,mutation_seq,$3,transaction_timestamp() FROM library_root WHERE id=$2", uuid.New(), f.root, mode)
			}
			got := f.read(t, 0).Roots[0]
			if got.CapacityState != state || got.Availability != availability {
				t.Fatalf("got %+v", got)
			}
			if state == "unknown" && (got.TotalBytes != nil || got.AvailableBytes != nil || got.SampledAt != nil) {
				t.Fatal("untrusted capacity exposed", got)
			}
			if state != "unknown" && (got.TotalBytes == nil || *got.TotalBytes != "9007199254740993" || got.AvailableBytes == nil || *got.AvailableBytes != "4007199254740993" || got.SampledAt == nil) {
				t.Fatal("historical bytes/time lost", got)
			}
		})
	}
}

func TestLibraryOverviewArtwork(t *testing.T) {
	f := newOverviewFixture(t)
	m, u := f.media(t, "movie")
	f.file(t, f.root, u, "imported", f.now(t))
	f.exec(t, "INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.movie','123')", uuid.New(), m)
	f.exec(t, "INSERT INTO media_artwork(media_id,provider,namespace,external_id,metadata_version,poster_path) SELECT id,'tmdb','tmdb.movie','123',metadata_version,'/Safe_1.jpg' FROM media WHERE id=$1", m)
	syncVersion := func() {
		t.Helper()
		f.exec(t, "UPDATE media_artwork SET metadata_version=(SELECT metadata_version FROM media WHERE id=$1) WHERE media_id=$1", m)
	}
	assert := func(want bool) {
		t.Helper()
		item := f.read(t, 0).Recent[0]
		if (item.PosterURL != nil) != want {
			t.Fatalf("poster: %+v", item)
		}
		if want && *item.PosterURL != "https://image.tmdb.org/t/p/w342/Safe_1.jpg" {
			t.Fatal(item)
		}
	}
	assert(true)
	f.exec(t, "UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1", m)
	assert(false)
	f.exec(t, "UPDATE media_artwork SET metadata_version=(SELECT metadata_version FROM media WHERE id=$1) WHERE media_id=$1", m)
	assert(true)
	f.exec(t, "INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.movie','456')", uuid.New(), m)
	syncVersion()
	assert(false)
	f.exec(t, "DELETE FROM media_identity WHERE media_id=$1 AND external_id='456'", m)
	syncVersion()
	f.exec(t, "UPDATE media_artwork SET namespace='tmdb.tv' WHERE media_id=$1", m)
	assert(false)
	f.exec(t, "UPDATE media_artwork SET namespace='tmdb.movie',external_id='456' WHERE media_id=$1", m)
	assert(false)
	f.exec(t, "DELETE FROM media_identity WHERE media_id=$1", m)
	syncVersion()
	assert(false)
	// Simulate malformed legacy storage; domain URL validation must still reject it.
	f.exec(t, "ALTER TABLE media_artwork DROP CONSTRAINT media_artwork_poster_path_check, DROP CONSTRAINT media_artwork_external_id_check")
	f.exec(t, "INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.movie','123')", uuid.New(), m)
	syncVersion()
	for _, path := range []string{"https://evil.invalid/a.jpg", "//evil.invalid/a.jpg", "/../a.jpg", "/a.jpg?secret=x", "/a%2Ejpg", "/a.svg"} {
		f.exec(t, "UPDATE media_artwork SET external_id='123',poster_path=$2 WHERE media_id=$1", m, path)
		assert(false)
	}
	f.exec(t, "UPDATE media_identity SET external_id='0123' WHERE media_id=$1", m)
	syncVersion()
	f.exec(t, "UPDATE media_artwork SET external_id='0123',poster_path='/Safe_1.jpg' WHERE media_id=$1", m)
	assert(false)
	f.exec(t, "DELETE FROM media_artwork WHERE media_id=$1", m)
	assert(false)
}

type overviewTrace struct {
	mu         sync.Mutex
	statements []string
	once       sync.Once
	mutate     func()
}

func (tr *overviewTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	tr.statements = append(tr.statements, data.SQL)
	tr.mu.Unlock()
	if strings.Contains(data.SQL, "-- name: CountLibraryOverview") && tr.mutate != nil {
		tr.once.Do(tr.mutate)
	}
	return ctx
}
func (*overviewTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestLibraryOverviewAuthorizationSnapshotAndStorageErrors(t *testing.T) {
	f := newOverviewFixture(t)
	ctx := context.Background()
	for _, actor := range []*ports.Actor{nil, {Type: ports.ActorTypeSystem, ID: f.actor.ID, Role: "admin"}, {Type: ports.ActorTypeService, ID: f.actor.ID, Role: "admin"}, {Type: ports.ActorTypeUser, ID: "malformed", Role: "member"}, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: "member"}, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: "admin"}, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: "invented"}, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: "member", Scopes: []string{}}, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: "member", Scopes: []string{"write"}}} {
		if out, err := f.store.ReadLibraryOverview(ctx, actor, ports.LibraryOverviewRequest{}); !errors.Is(err, ports.ErrForbidden) || out != nil {
			t.Fatalf("actor %+v: %+v %v", actor, out, err)
		}
	}
	for _, limit := range []int{-1, 25} {
		if _, err := f.store.ReadLibraryOverview(ctx, f.actor, ports.LibraryOverviewRequest{RecentLimit: limit}); !errors.Is(err, ports.ErrInvalidLibraryQuery) {
			t.Fatal(err)
		}
	}
	f.actor.Scopes = []string{"read"}
	f.read(t, 0)
	f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID))
	if _, err := f.store.ReadLibraryOverview(ctx, f.actor, ports.LibraryOverviewRequest{}); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal(err)
	}
	f.exec(t, "UPDATE app_user SET is_active=true,role='admin' WHERE id=$1", uuid.MustParse(f.actor.ID))
	if _, err := f.store.ReadLibraryOverview(ctx, f.actor, ports.LibraryOverviewRequest{}); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("stale role", err)
	}
	f.actor.Role = "admin"
	f.read(t, 0)
	_, u := f.media(t, "movie")
	file := f.file(t, f.root, u, "imported", f.now(t))
	trace := &overviewTrace{}
	trace.mutate = func() {
		f.exec(t, "UPDATE unit_fact SET revoked_at=transaction_timestamp() WHERE source_file_id=$1", file)
	}
	config, err := pgxpool.ParseConfig(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewLibraryOverviewStore(pool)
	before := f.now(t)
	out, err := store.ReadLibraryOverview(ctx, f.actor, ports.LibraryOverviewRequest{})
	after := f.now(t)
	if err != nil || out.Counts.Movies != "1" || out.ObservedAt.Before(before) || out.ObservedAt.After(after) {
		t.Fatalf("consistent snapshot/time: %+v %v", out, err)
	}
	if f.read(t, 0).Counts.Movies != "0" {
		t.Fatal("concurrent revocation did not commit")
	}
	trace.mu.Lock()
	statements := append([]string(nil), trace.statements...)
	trace.mu.Unlock()
	if len(statements) == 0 || !strings.Contains(strings.ToLower(statements[0]), "isolation level repeatable read read only") {
		t.Fatal("transaction mode", statements)
	}
	for _, sql := range statements {
		lower := strings.ToLower(sql)
		for _, verb := range []string{"insert into", "update ", "delete from"} {
			if strings.Contains(lower, verb) {
				t.Fatal("reader mutation", sql)
			}
		}
	}
	// A read-only database role verifies no persistence side effects are required.
	f.exec(t, "CREATE ROLE overview_readonly")
	t.Cleanup(func() {
		for _, sql := range []string{"DROP OWNED BY overview_readonly", "DROP ROLE overview_readonly"} {
			if _, err := f.pool.Exec(context.Background(), sql); err != nil {
				t.Error("owned role cleanup", err)
			}
		}
	})
	f.exec(t, "GRANT USAGE ON SCHEMA public TO overview_readonly")
	f.exec(t, "GRANT SELECT ON ALL TABLES IN SCHEMA public TO overview_readonly")
	config, err = pgxpool.ParseConfig(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["role"] = "overview_readonly"
	readonly, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = postgres.NewLibraryOverviewStore(readonly).ReadLibraryOverview(ctx, f.actor, ports.LibraryOverviewRequest{}); err != nil {
		readonly.Close()
		t.Fatal("SELECT-only reader", err)
	}
	readonly.Close()
	f.exec(t, "ALTER TABLE root_capacity_observation RENAME TO unavailable_capacity")
	out, err = f.store.ReadLibraryOverview(ctx, f.actor, ports.LibraryOverviewRequest{})
	if out != nil || err != ports.ErrUnavailable {
		t.Fatal("storage error escaped", out, err)
	}
	f.exec(t, "ALTER TABLE unavailable_capacity RENAME TO root_capacity_observation")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = f.store.ReadLibraryOverview(cancelled, f.actor, ports.LibraryOverviewRequest{}); err != ports.ErrUnavailable {
		t.Fatal("cancelled storage", err)
	}
	for _, store := range []*postgres.LibraryOverviewStore{nil, postgres.NewLibraryOverviewStore(nil)} {
		if _, err = store.ReadLibraryOverview(ctx, f.actor, ports.LibraryOverviewRequest{}); err != ports.ErrUnavailable {
			t.Fatal(err)
		}
	}
}

func TestLibraryOverviewMigrationRoundtrip(t *testing.T) {
	f := newOverviewFixture(t, 92)
	dir := repositoryMigrationsThrough(t, 92)
	_, u := f.media(t, "movie")
	f.file(t, f.root, u, "imported", f.now(t))
	before := f.read(t, 0)
	if version, err := postgres.Rollback(context.Background(), f.dsn, dir); err != nil || version != 91 {
		t.Fatalf("rollback: %d %v", version, err)
	}
	if out, err := f.store.ReadLibraryOverview(context.Background(), f.actor, ports.LibraryOverviewRequest{}); out != nil || err != ports.ErrUnavailable {
		t.Fatal("missing observation schema", out, err)
	}
	if version, err := postgres.Migrate(context.Background(), f.dsn, dir); err != nil || version != 92 {
		t.Fatalf("migrate: %d %v", version, err)
	}
	after := f.read(t, 0)
	if before.Counts != after.Counts || !reflect.DeepEqual(before.Recent, after.Recent) {
		t.Fatal("roundtrip changed library facts", fmt.Sprint(before), fmt.Sprint(after))
	}
}
