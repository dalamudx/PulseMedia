package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Exercise the real DB/Go timezone mismatch in a subprocess so changing TZ
// cannot alter the clocks or globals of concurrent package tests.
func TestMovieUpgradeOrganizationTimezoneReceiptPG(t *testing.T) {
	if os.Getenv("PULSE_UPGRADE_TIMEZONE_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestMovieUpgradeOrganization(TimezoneReceipt|ExecutionNative)PG$", "-test.v", "-test.timeout=80s")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "TZ=") && !strings.HasPrefix(value, "PULSE_UPGRADE_TIMEZONE_CHILD=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "TZ=Asia/Shanghai", "PULSE_UPGRADE_TIMEZONE_CHILD=1")
		cmd.WaitDelay = time.Second
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("non-UTC receipt recovery: %v\n%s", err, output)
		}
		t.Log(strings.TrimSpace(string(output)))
		return
	}
	f := physicalOrganizationFixture75(t)
	ctx := t.Context()
	const key = "timezone-prepare"
	initial, err := f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, key)
	if err != nil {
		t.Fatal(err)
	}
	_, offset := initial.PreparedAt.Zone()
	if offset != 8*60*60 {
		t.Fatal("fixture did not exercise a non-UTC Go timestamp")
	}
	var rowTime time.Time
	var original []byte
	if err = f.p.QueryRow(ctx, "SELECT (to_jsonb(x)->>'prepared_at')::timestamptz FROM movie_upgrade_organization_preparation x WHERE id=$1", initial.ID).Scan(&rowTime); err != nil {
		t.Fatal(err)
	}
	if !rowTime.Equal(initial.PreparedAt) {
		t.Fatal("timestamp instant changed")
	}
	if err = f.p.QueryRow(ctx, "SELECT response_body FROM idempotency_record WHERE actor_id=$1 AND idempotency_key=$2", f.actor.ID, "upgrade.organization-prepare:"+key).Scan(&original); err != nil {
		t.Fatal(err)
	}
	replay, err := f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, key)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.preparation.ReadMovieUpgradeOrganizationPreparationReceipt(ctx, f.actor, f.operation, key)
	if err != nil {
		t.Fatal(err)
	}
	initialJSON, _ := json.Marshal(initial)
	for _, recovered := range []any{replay, receipt} {
		raw, err := json.Marshal(recovered)
		if err != nil || !bytes.Equal(initialJSON, raw) {
			t.Fatal("original receipt representation changed", err)
		}
	}
	var after []byte
	if err = f.p.QueryRow(ctx, "SELECT response_body FROM idempotency_record WHERE actor_id=$1 AND idempotency_key=$2", f.actor.ID, "upgrade.organization-prepare:"+key).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("immutable receipt history changed")
	}
	f.planCount(t, 1)
}
