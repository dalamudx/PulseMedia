package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type pausedUpgradeSendDriver struct {
	ports.MovieUpgradeNativeSendDriver
	entered chan struct{}
	release chan struct{}
}

func (d *pausedUpgradeSendDriver) OpenMovieUpgradeNativeSend(ctx context.Context, desc ports.MovieUpgradeNativeDescriptor) (ports.MovieUpgradeNativeSendAttempt, error) {
	a, e := d.MovieUpgradeNativeSendDriver.OpenMovieUpgradeNativeSend(ctx, desc)
	if e != nil {
		return nil, e
	}
	return &pausedUpgradeSendAttempt{MovieUpgradeNativeSendAttempt: a, driver: d}, nil
}

type pausedUpgradeSendAttempt struct {
	ports.MovieUpgradeNativeSendAttempt
	driver *pausedUpgradeSendDriver
}

func (a *pausedUpgradeSendAttempt) ExecuteMovieUpgradeSend(ctx context.Context, p ports.MovieUpgradeNativeDispatchProof) (ports.MovieUpgradeSendReturnProof, error) {
	close(a.driver.entered)
	select {
	case <-a.driver.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return a.MovieUpgradeNativeSendAttempt.ExecuteMovieUpgradeSend(ctx, p)
}

func TestMovieUpgradeSendJobRejectPreMigrationDispatchProofPG(t *testing.T) {
	f := nativeUpgrade(t, "Ok.", false)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	issuer := postgres.NewMovieUpgradeNativeDispatchVerifier()
	driver, e := download.NewMovieUpgradeNativeSendDriver(f.manager, issuer, nativeUpgradeClock{})
	if e != nil {
		t.Fatal(e)
	}
	paused := &pausedUpgradeSendDriver{MovieUpgradeNativeSendDriver: driver, entered: make(chan struct{}), release: make(chan struct{})}
	repo, e := postgres.NewMovieUpgradeNativeSendRepository(f.preparation, paused, issuer)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	drained := false
	var once sync.Once
	release := func() { once.Do(func() { close(paused.release) }) }
	go func() {
		_, err := repo.SendMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), f.proof, "pre71-dispatch")
		done <- err
	}()
	defer func() {
		release()
		if !drained {
			<-done
		}
	}()
	select {
	case <-paused.entered:
	case e = <-done:
		drained = true
		t.Fatal("dispatch not reached before71", e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if f.adds.Load() != 0 {
		t.Fatal("Add started before proof consumption")
	}
	if v, e := postgres.Migrate(ctx, f.dsn, send71MigrationDir(t)); e != nil || v != 71 {
		t.Fatal(v, e)
	}
	release()
	e = <-done
	drained = true
	if e == nil || f.adds.Load() != 0 {
		t.Fatal("commandless old proof authorized Add after71", e, f.adds.Load())
	}
	var retained bool
	if e = f.p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_authorization WHERE operation_id=$1) AND EXISTS(SELECT 1 FROM movie_upgrade_send_dispatch WHERE operation_id=$1) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_job WHERE operation_id=$1) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=$1)`, f.operation).Scan(&retained); e != nil || !retained {
		t.Fatal("migration consumed or rewrote old evidence", e)
	}
}
