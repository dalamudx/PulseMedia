package httpapi_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func TestPublicUpgradePreparationReplayRevokedDuringWaitPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	f.prepared(t, "revoke-prepared-receipt")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := f.a.DBPool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", uuid.MustParse(f.actor.ID)); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.command("upgrade-preparations", f.body(), "revoke-prepared-receipt", f.admin, nil) }()
	selectionWaitFor(t, func() bool {
		var waiting bool
		_ = f.a.DBPool().QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%app_user%')").Scan(&waiting)
		return waiting
	})
	if _, err = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != 403 {
			t.Fatalf("revoked recovery %d %s", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if f.fetches.Load() != 1 {
		t.Fatal("revoked recovery fetched")
	}
	f.noSend(t, 1)
}

func TestPublicUpgradePreparationConcurrentInitialPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	f.onFetch = func() { close(started); <-release }
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.command("upgrade-preparations", f.body(), "one-original-command", f.admin, nil) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("fetch did not start")
	}
	concurrent := f.command("upgrade-preparations", f.body(), "one-original-command", f.admin, nil)
	close(release)
	if concurrent.Code != 409 {
		t.Fatalf("in-flight duplicate %d %s", concurrent.Code, concurrent.Body.String())
	}
	select {
	case first := <-done:
		if first.Code != 202 {
			t.Fatalf("initial %d %s", first.Code, first.Body.String())
		}
		recovered := f.prepared(t, "one-original-command")
		if first.Body.String() != recovered.Body.String() {
			t.Fatal("recovery changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not return")
	}
	if f.fetches.Load() != 1 {
		t.Fatal("concurrent request fetched again")
	}
	f.noSend(t, 1)
}

func TestPublicUpgradePreparationProjectionDuringFetchPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	ctx := context.Background()
	var wanted uuid.UUID
	if err := f.a.DBPool().QueryRow(ctx, "SELECT id FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2", f.request.SubscriptionID, f.unit).Scan(&wanted); err != nil {
		t.Fatal(err)
	}
	sub, err := postgres.Queries(f.a.DBPool()).GetSubscriptionByID(ctx, f.request.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	var projectionErr error
	f.onFetch = func() {
		_, projectionErr = postgres.NewWantedProjectionStore(f.a.DBPool()).ProjectWanted(ctx, wanted, sub.QualityPolicyID, sub.QualityPolicyRevision)
		items, listErr := postgres.Queries(f.a.DBPool()).ListPendingEvaluationWantedItems(ctx, 25)
		if listErr != nil {
			projectionErr = listErr
			return
		}
		for _, item := range items {
			if item.ID == wanted {
				projectionErr = errors.New("leased preparation occupies evaluation batch")
			}
		}
	}
	w := f.command("upgrade-preparations", f.body(), "projection-during-fetch", f.admin, nil)
	if w.Code != 202 || !errors.Is(projectionErr, ports.ErrWantedProjectionChanged) {
		t.Fatalf("active preparation HTTP=%d projection=%v", w.Code, projectionErr)
	}
	f.noSend(t, 1)
	if _, err = postgres.NewWantedProjectionStore(f.a.DBPool()).ProjectWanted(ctx, wanted, sub.QualityPolicyID, sub.QualityPolicyRevision); !errors.Is(err, ports.ErrWantedProjectionChanged) {
		t.Fatalf("prepared confirmation must preserve projection: %v", err)
	}
	f.exec(t, "UPDATE wanted_item SET is_skipped=true WHERE id=$1", wanted)
	if _, err = postgres.NewWantedProjectionStore(f.a.DBPool()).ProjectWanted(ctx, wanted, sub.QualityPolicyID, sub.QualityPolicyRevision); err != nil {
		t.Fatalf("explicit skip did not resume projection: %v", err)
	}
}

func TestPublicUpgradePreparationExpiredLeaseRecoveryPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	first := f.prepared(t, "expired-preparation-receipt")
	var seconds float64
	if err := f.a.DBPool().QueryRow(context.Background(), "SELECT greatest(extract(epoch FROM lease_until-clock_timestamp()),0)::float8 FROM movie_upgrade_preparation_lease WHERE operation_id=$1", f.operation).Scan(&seconds); err != nil {
		t.Fatal(err)
	}
	if seconds > 61 {
		t.Fatal("unexpected preparation lease duration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	timer := time.NewTimer(time.Duration(seconds*float64(time.Second)) + 50*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var expired bool
	if err := f.a.DBPool().QueryRow(ctx, "SELECT lease_until<=clock_timestamp() FROM movie_upgrade_preparation_lease WHERE operation_id=$1", f.operation).Scan(&expired); err != nil || !expired {
		t.Fatalf("lease did not expire: %v", err)
	}
	recovered := f.prepared(t, "expired-preparation-receipt")
	if recovered.Body.String() != first.Body.String() || f.fetches.Load() != 1 {
		t.Fatal("expired historical recovery changed receipt or fetched")
	}
	f.noSend(t, 1)
}

func TestPublicUpgradePreparationMalformedMetainfoPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	f.raw = []byte("not-bencode")
	w := f.command("upgrade-preparations", f.body(), "malformed-metainfo", f.admin, nil)
	if w.Code != 409 {
		t.Fatalf("invalid metainfo %d %s", w.Code, w.Body.String())
	}
	if f.fetches.Load() != 1 {
		t.Fatal("invalid fetch count")
	}
	f.noSend(t, 0)
}
