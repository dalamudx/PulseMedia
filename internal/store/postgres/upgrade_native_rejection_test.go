package postgres_test

import (
	"context"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func TestMovieUpgradeNativeRejectedBeforeAddPG(t *testing.T) {
	for _, kind := range []string{"inactive_account", "offline_root", "no_factory", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			n := nativeUpgrade(t, "Ok.", false)
			ctx := context.Background()
			switch kind {
			case "inactive_account":
				n.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", n.actor.ID)
			case "offline_root":
				n.exec(t, "UPDATE library_root SET health='offline' WHERE id=$1", n.root)
			case "no_factory":
				n.manager.SetClientFactory(nil)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if _, e := n.repo.SendMovieUpgrade(ctx, n.actor, n.operation, uuid.New(), n.proof, "native-reject"); e == nil {
				t.Fatal("invalid command succeeded")
			}
			if n.adds.Load() != 0 {
				t.Fatal("Add before current authorization")
			}
			var count int
			if e := n.p.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_send_authorization)+(SELECT count(*) FROM movie_upgrade_send_dispatch)+(SELECT count(*) FROM movie_upgrade_send_result)`).Scan(&count); e != nil || count != 0 {
				t.Fatal("failed command retained physical authority", count, e)
			}
		})
	}
}

func TestMovieUpgradeNativeDispatchRejectsCallerProof(t *testing.T) {
	a, b := postgres.NewMovieUpgradeNativeDispatchVerifier(), postgres.NewMovieUpgradeNativeDispatchVerifier()
	if a == b {
		t.Fatal("distinct native issuers share identity")
	}
	for _, claim := range []any{nil, struct{}{}, map[string]any{"committed": true}} {
		if _, e := a.ConsumeMovieUpgradeDispatch(context.Background(), claim, ports.MovieUpgradeNativeDescriptor{}); e == nil {
			t.Fatal("caller supplied dispatch accepted")
		}
	}
}
