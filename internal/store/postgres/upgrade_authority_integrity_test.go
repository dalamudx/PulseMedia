package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestFrozenUpgradeAuthorityInsertIntegrityPG(t *testing.T) {
	for _, field := range []string{"protected_digest", "client_digest"} {
		t.Run(field, func(t *testing.T) {
			f := frozenUpgrade(t)
			f.enable(t)
			command := f.command(t)
			before := f.oldState(t)
			f.exec(t, fmt.Sprintf(`CREATE FUNCTION test_upgrade_authority_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.snapshot := jsonb_set(NEW.snapshot, '{%s}', to_jsonb(repeat('f',64))); RETURN NEW; END $$; CREATE TRIGGER test_upgrade_authority_insert BEFORE INSERT ON movie_upgrade_authority FOR EACH ROW EXECUTE FUNCTION test_upgrade_authority_insert()`, field))
			result, err := f.store.ReserveUpgrade(context.Background(), f.actor, command, "altered-authority")
			if !errors.Is(err, ports.ErrUpgradeReservationConflict) {
				t.Fatalf("trigger-altered %s accepted: result=%+v err=%v", field, result, err)
			}
			f.assertNoReservation(t)
			var events int
			if err := f.p.QueryRow(context.Background(), "SELECT count(*) FROM outbox_event WHERE event_type='upgrade.reserve'").Scan(&events); err != nil || events != 0 {
				t.Fatalf("failed authority left events=%d: %v", events, err)
			}
			if f.oldState(t) != before {
				t.Fatal("failed authority changed original file or facts")
			}
		})
	}
}
