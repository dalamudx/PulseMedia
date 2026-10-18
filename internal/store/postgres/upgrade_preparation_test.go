package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"strings"
	"sync"
	"testing"
)

type preparedUpgradeFixture struct {
	*frozenUpgradeFixture
	preparation *postgres.UpgradePreparationStore
	proof       *torrent.MovieUpgradeMetainfoProof
	operation   uuid.UUID
	lease       *ports.MovieUpgradePreparationLease
}

func preparingUpgrade(t *testing.T) *preparedUpgradeFixture {
	return preparingUpgradeWithHook(t, nil)
}
func preparingUpgradeWithHook(t *testing.T, beforeFreeze func(*frozenUpgradeFixture)) *preparedUpgradeFixture {
	t.Helper()
	f := frozenUpgrade(t)
	ctx := context.Background()
	if v, e := postgres.Migrate(ctx, f.dsn, preparationMigrationDir(t)); e != nil || v != 65 {
		t.Fatalf("migrate65: %d %v", v, e)
	}
	f.enable(t)
	var medium uuid.UUID
	if e := f.p.QueryRow(ctx, "SELECT media_id FROM media_unit WHERE id=$1", f.unit).Scan(&medium); e != nil {
		t.Fatal(e)
	}
	f.exec(t, "INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.movie','550')", uuid.New(), medium)
	f.project(t)
	q := db.New(f.p)
	m, e := q.GetMediaByID(ctx, medium)
	if e != nil {
		t.Fatal(e)
	}
	units, e := q.ListUpgradeUnits(ctx, medium)
	if e != nil || len(units) != 1 {
		t.Fatal(units, e)
	}
	target := media.MovieUpgradeTarget{MediaID: medium, UnitID: f.unit, MediaVersion: m.MetadataVersion, UnitVersion: units[0].MetadataVersion, MediaType: media.TypeMovie, UnitKey: "movie", OriginalTitle: m.Title, Year: int(m.Year.Int32), Identities: []media.Identity{{Namespace: "tmdb.movie", ExternalID: "550"}}, ParserVersion: media.EpisodeParserVersion}
	name := "Fight.Club.1999.2160p.Bluray.x265.mkv"
	raw := []byte(fmt.Sprintf("d8:announce44:https://tracker.invalid/private-passkey/abcd4:infod6:lengthi4e4:name%d:%s12:piece lengthi4e6:pieces20:00000000000000000000ee", len(name), name))
	proof, e := torrent.InspectMovieUpgradeMetainfo(raw, target)
	if e != nil {
		t.Fatalf("native proof target=%+v: %v", target, e)
	}
	f.exec(t, "UPDATE release_candidate SET size_bytes=4,info_hash=$2 WHERE id=$1", f.candidate, proof.InfoHash())
	if beforeFreeze != nil {
		beforeFreeze(f)
	}
	result, e := f.store.ReserveUpgrade(ctx, f.actor, f.command(t), "reserve")
	if e != nil {
		t.Fatalf("reserve upgrade fixture: %v", e)
	}
	repository, e := postgres.NewUpgradePreparationStore(f.p, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	out := &preparedUpgradeFixture{frozenUpgradeFixture: f, preparation: repository, proof: proof, operation: result.OperationID}
	out.lease, e = repository.ClaimMovieUpgradePreparation(ctx, f.actor, ports.ClaimMovieUpgradePreparation{OperationID: result.OperationID, Owner: uuid.New()})
	if e != nil {
		t.Fatalf("claim: %v", e)
	}
	return out
}
func (f *preparedUpgradeFixture) prepare() ports.PrepareMovieUpgrade {
	return ports.PrepareMovieUpgrade{OperationID: f.operation, Owner: f.lease.Owner, Token: f.lease.Token, Proof: f.proof}
}
func (f *preparedUpgradeFixture) assertUnprepared(t *testing.T) {
	t.Helper()
	var n int
	if e := f.p.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_preparation)+(SELECT count(*) FROM download_task)+(SELECT count(*) FROM audit_log WHERE action='upgrade.prepare')+(SELECT count(*) FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.prepare:%')`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("partial preparation: %d %v", n, e)
	}
}
func TestMovieUpgradePreparationPG(t *testing.T) {
	f := preparingUpgrade(t)
	before := f.oldState(t)
	r, e := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "prepare")
	if e != nil {
		t.Fatal(e)
	}
	if r.Status != "prepared_not_sent" || r.ExecutionAllowed || r.TaskID == uuid.Nil {
		t.Fatalf("result: %+v", r)
	}
	replay, e := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "prepare")
	if e != nil || *replay != *r {
		t.Fatal(replay, e)
	}
	if f.oldState(t) != before {
		t.Fatal("old file/facts changed")
	}
	var n int
	var state, path, payload string
	e = f.p.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM download_task),t.state,t.save_path,to_jsonb(p)::text FROM download_task t JOIN movie_upgrade_preparation p ON p.task_id=t.id WHERE t.operation_id=$1`, f.operation).Scan(&n, &state, &path, &payload)
	if e != nil || n != 1 || state != "submitting" || path != "/never-open-frozen-upgrade" {
		t.Fatal(n, state, path, e)
	}
	if strings.Contains(payload, "private-passkey") || strings.Contains(payload, "tracker.invalid") || strings.Contains(payload, "never-decrypt") {
		t.Fatal("secret persisted")
	}
}
func TestMovieUpgradePreparationConcurrencyPG(t *testing.T) {
	f := preparingUpgrade(t)
	var wg sync.WaitGroup
	rs := make(chan *ports.MovieUpgradePreparationResult, 8)
	es := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			r, e := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "same")
			rs <- r
			es <- e
		})
	}
	wg.Wait()
	close(rs)
	close(es)
	for e := range es {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first *ports.MovieUpgradePreparationResult
	for r := range rs {
		if r == nil {
			t.Fatal("nil result")
		}
		if first == nil {
			first = r
		} else if *r != *first {
			t.Fatal("different receipt")
		}
	}
}
func TestMovieUpgradePreparationStaleInputPG(t *testing.T) {
	for _, c := range []struct{ name, sql string }{
		{"file", `UPDATE library_file SET size_bytes=size_bytes+1 WHERE id=$1`},
		{"client", `UPDATE download_client SET remote_path_map='{"/x":"/y"}'::jsonb WHERE id=$1`},
		{"fact", `UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE id=$1`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := preparingUpgrade(t)
			id := f.file
			if c.name == "client" {
				id = f.client
			}
			if c.name == "fact" {
				id = f.fact
			}
			f.exec(t, c.sql, id)
			_, e := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "stale")
			if !errors.Is(e, ports.ErrUpgradeReservationConflict) {
				t.Fatal(e)
			}
			f.assertUnprepared(t)
		})
	}
	t.Run("token", func(t *testing.T) {
		f := preparingUpgrade(t)
		c := f.prepare()
		c.Token = uuid.New()
		_, e := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, c, "token")
		if !errors.Is(e, ports.ErrUpgradeReservationConflict) {
			t.Fatal(e)
		}
		f.assertUnprepared(t)
	})
}
func TestMovieUpgradePreparationTailRollbackPG(t *testing.T) {
	for _, point := range []string{"task", "preparation", "receipt"} {
		t.Run(point, func(t *testing.T) {
			f := preparingUpgrade(t)
			before := f.oldState(t)
			switch point {
			case "task":
				f.exec(t, `CREATE FUNCTION test_prep_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.save_path='/wrong'; RETURN NEW; END $$; CREATE TRIGGER test_prep_fault BEFORE INSERT ON download_task FOR EACH ROW EXECUTE FUNCTION test_prep_fault()`)
			case "preparation":
				f.exec(t, `CREATE FUNCTION test_prep_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.evidence=jsonb_set(NEW.evidence,'{save_path}','"/wrong"'::jsonb); RETURN NEW; END $$; CREATE TRIGGER test_prep_fault BEFORE INSERT ON movie_upgrade_preparation FOR EACH ROW EXECUTE FUNCTION test_prep_fault()`)
			case "receipt":
				f.exec(t, `CREATE FUNCTION test_prep_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key LIKE 'upgrade.prepare:%' THEN UPDATE library_file SET size_bytes=size_bytes+1 WHERE id='`+f.file.String()+`'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_prep_fault BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION test_prep_fault()`)
			}
			_, e := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "fault")
			if !errors.Is(e, ports.ErrUpgradeReservationConflict) {
				t.Fatal(e)
			}
			f.assertUnprepared(t)
			if f.oldState(t) != before {
				t.Fatal("old file changed")
			}
		})
	}
}
func TestMovieUpgradePreparationHistoricalPermissionPG(t *testing.T) {
	f := preparingUpgrade(t)
	r, e := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "history")
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE subscription SET upgrade_mode='disabled' WHERE id=$1", f.sub)
	old, e := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "history")
	if e != nil || *old != *r {
		t.Fatal(old, e)
	}
	f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID))
	_, e = f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "history")
	if !errors.Is(e, ports.ErrForbidden) {
		t.Fatal(e)
	}
}
