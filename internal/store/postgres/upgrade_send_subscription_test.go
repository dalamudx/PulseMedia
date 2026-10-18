package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestMovieUpgradeSendSubscriptionBindingPG(t *testing.T) {
	for _, publicFirst := range []bool{false, true} {
		name := "legacy-key"
		if publicFirst {
			name = "public-key"
		}
		t.Run(name, func(t *testing.T) {
			f, repo, _, c := sendJobFixture(t, "Ok.")
			var err error
			var sub uuid.UUID
			if err = f.p.QueryRow(context.Background(), "SELECT subscription_id FROM movie_upgrade_authority WHERE operation_id=$1", f.operation).Scan(&sub); err != nil {
				t.Fatal(err)
			}
			c.SubscriptionID = uuid.New()
			if _, err = repo.QueueMovieUpgradeSend(context.Background(), f.actor, c, "binding-send"); !errors.Is(err, ports.ErrUpgradeReservationConflict) {
				t.Fatalf("wrong route before confirmation: %v", err)
			}
			var n int
			if err = f.p.QueryRow(context.Background(), "SELECT count(*) FROM movie_upgrade_send_job WHERE operation_id=$1", f.operation).Scan(&n); err != nil || n != 0 {
				t.Fatal("wrong route created command", n, err)
			}
			c.SubscriptionID = uuid.Nil
			if publicFirst {
				c.SubscriptionID = sub
			}
			first, err := repo.QueueMovieUpgradeSend(context.Background(), f.actor, c, "binding-send")
			if err != nil {
				t.Fatal(err)
			}
			c.SubscriptionID = uuid.New()
			if _, err = repo.QueueMovieUpgradeSend(context.Background(), f.actor, c, "binding-send"); !errors.Is(err, ports.ErrUpgradeReservationConflict) {
				t.Fatalf("wrong route recovered receipt: %v", err)
			}
			if publicFirst {
				c.SubscriptionID = uuid.Nil
			} else {
				c.SubscriptionID = sub
			}
			replay, err := repo.QueueMovieUpgradeSend(context.Background(), f.actor, c, "binding-send")
			if err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(first)
			b, _ := json.Marshal(replay)
			if string(a) != string(b) {
				t.Fatal("route binding changed original receipt/fingerprint")
			}
		})
	}
}
