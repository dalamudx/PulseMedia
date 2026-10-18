package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"testing"
)

func TestMovieUpgradeSendArmFactExpiryPG(t *testing.T) {
	f := preparingUpgradeWithHook(t, func(f *frozenUpgradeFixture) {
		f.exec(t, `UPDATE unit_fact SET expires_at=clock_timestamp()+interval '8 seconds' WHERE id=$1`, f.fact)
		f.project(t)
	})
	if _, e := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "expiry-prepare"); e != nil {
		t.Fatal(e)
	}
	if v, e := postgres.Migrate(context.Background(), f.dsn, sendArmMigrationDir(t)); e != nil || v != 66 {
		t.Fatal(v, e)
	}
	s := armSession(t, f)
	f.exec(t, `CREATE FUNCTION test_arm_expiry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key LIKE 'upgrade.arm:%' THEN PERFORM pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM ((SELECT expires_at FROM unit_fact WHERE id='`+f.fact.String()+`')-clock_timestamp())))+0.03); END IF; RETURN NEW; END $$;CREATE TRIGGER test_arm_expiry BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION test_arm_expiry()`)
	before := f.oldState(t)
	_, e := s.ArmMovieUpgradeSend(context.Background(), f.actor, uuid.New(), f.proof, "expiry-tail")
	if !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	noArm(t, f)
	if f.oldState(t) != before {
		t.Fatal("expiration changed old file or facts")
	}
}
