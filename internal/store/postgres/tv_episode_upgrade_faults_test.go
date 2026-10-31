package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

func TestTVEpisodeUpgradeConcurrentReservationPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	preview, e := f.service.Preview(t.Context(), f.actor, f.request)
	if e != nil || !preview.Improvement {
		t.Fatal(preview, e)
	}
	command := ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "reserve", ExpectedVersion: preview.Version, Confirmed: true}
	results := make(chan *ports.TVEpisodeUpgradeResult, 6)
	errs := make(chan error, 6)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			r, e := f.service.Command(t.Context(), f.actor, command, "same-reserve")
			results <- r
			errs <- e
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var operation uuid.UUID
	for r := range results {
		if r == nil {
			t.Fatal("missing receipt")
		}
		if operation == uuid.Nil {
			operation = r.OperationID
		}
		if operation != r.OperationID {
			t.Fatal("duplicate authority")
		}
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_authority"); n != 1 {
		t.Fatal(n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL"); n != 1 {
		t.Fatal(n)
	}
}
func TestTVEpisodeUpgradeLostOriginalSessionDoesNotReplayPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	r := f.prepared(t)
	f.native.before = func() {
		var stopped bool
		e := f.pool.QueryRow(context.Background(), `SELECT pg_terminate_backend(backend_pid) FROM tv_episode_upgrade_permit WHERE operation_id=$1 AND action='send'`, r.OperationID).Scan(&stopped)
		if e != nil || !stopped {
			t.Errorf("owned session termination: %v %v", stopped, e)
		}
	}
	_, e := f.service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "send", OperationID: r.OperationID, ExpectedVersion: r.Version, Confirmed: true}, "lost-session")
	if e == nil {
		t.Fatal("lost session recorded success")
	}
	_, e = f.service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "send", OperationID: r.OperationID, ExpectedVersion: r.Version, Confirmed: true}, "new-session")
	if e == nil || f.native.calls.Load() != 1 {
		t.Fatal("lost session allowed Add replay", e, f.native.calls.Load())
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_return WHERE action='send'"); n != 0 {
		t.Fatal("untrusted return persisted", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL"); n != 1 {
		t.Fatal("lost session released protection", n)
	}
}

type tvUpgradeFailPublisher struct{}

func (tvUpgradeFailPublisher) PublishImported(context.Context, *db.Queries, string, any) (*db.OutboxEvent, error) {
	return nil, errors.New("owned import rollback probe")
}
func TestTVEpisodeUpgradeIngestionFailurePreservesOldVideoPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	r := f.prepared(t)
	f.command(t, "send", r.OperationID, r.Version, "send")
	f.command(t, "observe", r.OperationID, r.Version, "observe")
	review := f.command(t, "organization-preview", r.OperationID, r.Version, "review")
	confirmed := f.command(t, "organization-confirm", r.OperationID, review.Version, "confirm")
	fs := rootfs.NewFilesystem()
	repo := postgres.NewTVEpisodeUpgradeStore(f.pool, idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32)), f.native, fs, fs, tvUpgradeFailPublisher{})
	service := acquisition.NewTVEpisodeUpgradeService(repo)
	_, e := service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "organize", OperationID: r.OperationID, ExpectedVersion: confirmed.Version, Confirmed: true}, "organize-fails")
	if e == nil {
		t.Fatal("failed import committed")
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_phase WHERE action='organize'"); n != 0 {
		t.Fatal("rollback left success", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM library_file_unit WHERE unit_id=$1 AND retired_at IS NULL", f.unit); n != 1 {
		t.Fatal("rollback changed old mapping", n)
	}
	var old string
	if e = f.pool.QueryRow(t.Context(), "SELECT rel_path FROM library_file WHERE id=$1 AND retired_at IS NULL", f.request.CurrentFileID).Scan(&old); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(f.rootPath, old))
	if e != nil || !bytes.Equal(data, bytes.Repeat([]byte("o"), 100)) {
		t.Fatal("failed ingestion touched old video", e)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_permit WHERE action='organize'"); n != 1 {
		t.Fatal("physical permit was lost", n)
	}
	_, e = service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "retirement-preview", OperationID: r.OperationID, Confirmed: true}, "forbidden-retire")
	if e == nil {
		t.Fatal("retirement authorized without ingestion")
	}
}