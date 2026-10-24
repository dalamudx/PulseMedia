package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func setFixtureNaming(t *testing.T, f *safetyFixture, rules naming.TemplateRules) int64 {
	t.Helper()
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.q.SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: raw})
	if err != nil {
		t.Fatal(err)
	}
	return row.Version
}

func TestNamingConfigurationFrozenPlanAndRecoveryPG(t *testing.T) {
	f := safetySetup(t)
	version := setFixtureNaming(t, f, naming.TemplateRules{MovieTemplate: "Movies/{title}/main.{extension}"})
	plan, step := f.plan(t)
	if step.TargetPath != filepath.Join(f.target, "Movies/Movie/main.mkv") {
		t.Fatal("new plan ignored persisted naming")
	}
	var evidence transferTargetEvidence
	if json.Unmarshal(step.TargetEvidence, &evidence) != nil || evidence.NamingVersion != version {
		t.Fatal("naming version not frozen")
	}
	// A broken current setting cannot withdraw an already frozen plan's target.
	if _, err := f.q.SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ExecutePlan(t.Context(), plan.ID); err != nil {
		t.Fatal("new setting blocked old plan", err)
	}
	source, err := os.Stat(f.sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Stat(step.TargetPath)
	if err != nil || !os.SameFile(source, target) {
		t.Fatal("frozen target was not the original hard link", err)
	}
	if err = f.svc.ExecutePlan(t.Context(), plan.ID); err != nil {
		t.Fatal("completed plan not recoverable", err)
	}
	steps, err := f.q.ListStepsForPlan(t.Context(), plan.ID)
	if err != nil || len(steps) != 1 || !bytes.Equal(steps[0].TargetEvidence, step.TargetEvidence) || steps[0].TargetPath != step.TargetPath {
		t.Fatal("recovery changed naming evidence", err)
	}
	f.oneImport(t, plan.ID)
}

func TestNamingConfigurationMissingFactsAndContainerPG(t *testing.T) {
	for _, test := range []struct{ name, template string }{
		{"unknown_year", "{title} ({year})/{title}.{extension}"},
		{"changed_container", "{title}.mp4"},
		{"filtered_target", "{title}/sample.{extension}"},
		{"unknown_identity", "{title} [{tmdb_id}].{extension}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := safetySetup(t)
			setFixtureNaming(t, f, naming.TemplateRules{MovieTemplate: test.template})
			// The fixture's locked media year is unknown; the caller cannot invent it.
			_, _, err := f.svc.CreateMoviePlan(t.Context(), uuid.New(), f.root, f.unit, f.sourcePath, "Caller title", 2020, quality.Profile{})
			if !errors.Is(err, ErrReviewRequired) {
				t.Fatal("invalid new target was authorized", err)
			}
			if f.count(t, "transfer_plan") != 0 || f.count(t, "transfer_step") != 0 {
				t.Fatal("rejected target left a partial plan")
			}
			f.noImport(t)
			if _, err = os.Stat(f.sourcePath); err != nil {
				t.Fatal("source changed", err)
			}
		})
	}
}

func TestNamingConfigurationWaitUsesCommittedVersionPG(t *testing.T) {
	f := safetySetup(t)
	setFixtureNaming(t, f, naming.TemplateRules{MovieTemplate: "First/{title}.{extension}"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	queries := f.q.WithTx(tx)
	if err = queries.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "setting", AggregateID: naming.TemplateSettingKey}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(naming.TemplateRules{MovieTemplate: "Committed/{title}.{extension}"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := queries.SetSetting(ctx, db.SetSettingParams{Key: naming.TemplateSettingKey, Value: raw})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		step *db.TransferStep
		err  error
	}
	done := make(chan result, 1)
	go func() {
		_, step, err := f.svc.CreateMoviePlan(ctx, uuid.New(), f.root, f.unit, f.sourcePath, "Movie", 2020, quality.Profile{})
		done <- result{step, err}
	}()
	t.Cleanup(func() {
		cancel()
		_ = tx.Rollback(context.Background())
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("plan did not stop after cancellation")
		}
	})
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
waiting:
	for {
		var waiting bool
		if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break waiting
		}
		select {
		case result := <-done:
			done <- result
			t.Fatal("first plan did not wait for the configuration writer", result.err)
		case <-deadline.C:
			t.Fatal("configuration wait not reached")
		case <-ticker.C:
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		done <- result
		if result.err != nil || result.step == nil || result.step.TargetPath != filepath.Join(f.target, "Committed/Movie.mkv") {
			t.Fatal("plan did not use the committed version", result.err)
		}
		var evidence transferTargetEvidence
		if json.Unmarshal(result.step.TargetEvidence, &evidence) != nil || evidence.NamingVersion != current.Version {
			t.Fatal("wait froze the old version")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("plan remained blocked after commit")
	}
	f.noImport(t)
}

func TestNamingConfigurationNewPlanTailRollbackPG(t *testing.T) {
	f := safetySetup(t)
	setFixtureNaming(t, f, naming.TemplateRules{MovieTemplate: "Films/{title}.{extension}"})
	if _, err := f.pool.Exec(t.Context(), `CREATE FUNCTION reject_naming_step() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture-naming-step'; END $$; CREATE TRIGGER reject_naming_step BEFORE INSERT ON transfer_step FOR EACH ROW EXECUTE FUNCTION reject_naming_step()`); err != nil {
		t.Fatal(err)
	}
	operation := uuid.New()
	if _, _, err := f.svc.CreateMoviePlan(t.Context(), operation, f.root, f.unit, f.sourcePath, "Movie", 2020, quality.Profile{}); err == nil {
		t.Fatal("injected failure succeeded")
	}
	if f.count(t, "transfer_plan") != 0 || f.count(t, "transfer_step") != 0 {
		t.Fatal("tail failure left a half-plan")
	}
	f.noImport(t)
	if _, err := f.pool.Exec(t.Context(), `DROP TRIGGER reject_naming_step ON transfer_step`); err != nil {
		t.Fatal(err)
	}
	plan, step, err := f.svc.CreateMoviePlan(t.Context(), operation, f.root, f.unit, f.sourcePath, "Movie", 2020, quality.Profile{})
	if err != nil || plan == nil || step == nil || step.TargetPath != filepath.Join(f.target, "Films/Movie.mkv") {
		t.Fatal("original operation did not recover", err)
	}
}
