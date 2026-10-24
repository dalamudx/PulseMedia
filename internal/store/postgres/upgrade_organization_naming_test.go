package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func setUpgradeNaming(t *testing.T, f *physicalOrganizationFixture, template string) int64 {
	t.Helper()
	raw, err := json.Marshal(naming.TemplateRules{MovieTemplate: template})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.New(f.p).SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: raw})
	if err != nil {
		t.Fatal(err)
	}
	return row.Version
}
func TestMovieUpgradeNamingPreparationFrozenOfflineReceiptPG(t *testing.T) {
	f := physicalOrganizationFixture75(t)
	version := setUpgradeNaming(t, f, "Custom/{title} [{tmdb_id}]/{title} ({year}).{extension}")
	before, old := nativeProtectedGraph(t, f.preparedUpgradeFixture), f.oldState(t)
	result, err := f.preparation.PrepareMovieUpgradeOrganization(t.Context(), f.actor, f.command, "m9-upgrade-naming")
	if err != nil {
		t.Fatal("custom upgrade preparation", err)
	}
	f.planCount(t, 1)
	var prepared []byte
	var target string
	if err = f.p.QueryRow(t.Context(), `SELECT to_jsonb(p),s.target_path FROM movie_upgrade_organization_preparation p JOIN transfer_step s ON s.id=p.step_id WHERE p.id=$1`, result.ID).Scan(&prepared, &target); err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Snapshot struct {
			Physical struct {
				Version int64  `json:"naming_version"`
				Target  string `json:"target_rel"`
			} `json:"physical"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(prepared, &frozen) != nil || frozen.Snapshot.Physical.Version != version || frozen.Snapshot.Physical.Target != "Custom/Fight Club [550]/Fight Club (1999).mkv" || target != filepath.Join(f.rootPath, frozen.Snapshot.Physical.Target) {
		t.Fatal("upgrade target did not freeze verified identity/version")
	}
	if _, err = os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preparation published a file", err)
	}
	if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) || old != f.oldState(t) {
		t.Fatal("preparation changed original protection or old file")
	}
	if _, err = db.New(f.p).SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(f.sourceRoot, f.sourceRoot+".offline"); err != nil {
		t.Fatal(err)
	}
	replay, err := f.preparation.PrepareMovieUpgradeOrganization(t.Context(), f.actor, f.command, "m9-upgrade-naming")
	if err != nil || !sameHistoricalJSON(replay, result) {
		t.Fatal("old preparation re-read current template or offline source", err)
	}
	read, err := f.preparation.ReadMovieUpgradeOrganizationPreparationReceipt(t.Context(), f.actor, f.operation, "m9-upgrade-naming")
	if err != nil || !sameHistoricalJSON(read, result) {
		t.Fatal("old original-key read changed receipt", err)
	}
	var after []byte
	if err = f.p.QueryRow(t.Context(), `SELECT to_jsonb(p) FROM movie_upgrade_organization_preparation p WHERE p.id=$1`, result.ID).Scan(&after); err != nil || !bytes.Equal(prepared, after) {
		t.Fatal("original preparation bytes changed", err)
	}
	f.planCount(t, 1)
	if old != f.oldState(t) {
		t.Fatal("original file changed during recovery")
	}
}

func TestMovieUpgradeNamingInvalidTargetKeepsProtectionPG(t *testing.T) {
	for _, template := range []string{"Custom/{title}.mp4", "Custom/{title}/{imdb_id}.{extension}"} {
		t.Run(template, func(t *testing.T) {
			f := physicalOrganizationFixture75(t)
			setUpgradeNaming(t, f, template)
			before, old := nativeProtectedGraph(t, f.preparedUpgradeFixture), f.oldState(t)
			if _, err := f.preparation.PrepareMovieUpgradeOrganization(t.Context(), f.actor, f.command, "m9-upgrade-invalid"); !errors.Is(err, ports.ErrUpgradeReservationConflict) {
				t.Fatal("invalid target accepted or misclassified", err)
			}
			f.planCount(t, 0)
			if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) || old != f.oldState(t) {
				t.Fatal("invalid target changed protected rows")
			}
		})
	}
}

func TestMovieUpgradeNamingChangeDuringObservationPG(t *testing.T) {
	f := physicalOrganizationFixture75(t)
	const template = "Same/{title}/{title}.{extension}"
	first := setUpgradeNaming(t, f, template)
	fs := physicalBarrier(f)
	repo := physicalPlanRepository(t, f.p, fs)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	var once sync.Once
	release := func() { once.Do(func() { close(fs.proceed) }) }
	done := make(chan error, 1)
	joined := false
	t.Cleanup(func() {
		cancel()
		release()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("owned preparation did not stop")
			}
		}
	})
	go func() {
		_, err := repo.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "m9-upgrade-version-race")
		done <- err
	}()
	select {
	case <-fs.entered:
	case <-ctx.Done():
		t.Fatal("physical observation did not begin", ctx.Err())
	}
	// The path is identical. Only the independently committed version has changed.
	second := setUpgradeNaming(t, f, template)
	if second <= first {
		t.Fatal("fixture did not publish a newer version")
	}
	release()
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, ports.ErrUpgradeReservationConflict) {
			t.Fatal("version change during observation was accepted", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	f.planCount(t, 0)
	result, err := f.preparation.PrepareMovieUpgradeOrganization(t.Context(), f.actor, f.command, "m9-upgrade-version-race")
	if err != nil {
		t.Fatal("rolled-back original key could not prepare current version", err)
	}
	var version int64
	if err = f.p.QueryRow(t.Context(), `SELECT (snapshot->'physical'->>'naming_version')::bigint FROM movie_upgrade_organization_preparation WHERE id=$1`, result.ID).Scan(&version); err != nil || version != second {
		t.Fatal("retry did not freeze the new committed version", err)
	}
	f.planCount(t, 1)
}
