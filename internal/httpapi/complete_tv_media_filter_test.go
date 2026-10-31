package httpapi_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
)

func TestCompleteTVOrganizationMediaSizeBoundaryPG(t *testing.T) {
	for _, mode := range []string{"at_threshold", "below_threshold"} {
		t.Run(mode, func(t *testing.T) {
			f := newCompleteOrganizationFixture(t)
			minimum := int64(512)
			if mode == "below_threshold" {
				minimum++
			}
			ids := idgen.New()
			repository := transferstore.NewRepositoryWithMinimumMediaBytes(f.n.f.a.DBPool(), ids, outbox.New(f.n.f.a.DBPool(), ids, clock.New()), rootfs.NewFilesystem(), minimum, transfer.ExecutePreparedPlan)
			service := transfer.NewCompleteTVOrganizationService(repository)
			snapshot := func() string {
				t.Helper()
				var raw string
				err := f.n.f.a.DBPool().QueryRow(t.Context(), `SELECT jsonb_build_array(
    (SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM transfer_plan p),
    (SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM transfer_step s),
    (SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM job j),
    (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM download_task t),
    (SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM download_requirement d),
    (SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM acquisition_reservation a),
    (SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM library_file f),
    (SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM unit_fact f),
    (SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM audit_log a),
    (SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM outbox_event e))::text`).Scan(&raw)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			before := snapshot()
			receipt, err := service.Confirm(t.Context(), f.n.actor, f.command, "size-boundary")
			if mode == "below_threshold" {
				if !errors.Is(err, transferstore.ErrReviewRequired) || receipt != nil {
					t.Fatal("filtered bundle silently dropped a mapped episode", err)
				}
				if before != snapshot() {
					t.Fatal("filtered bundle changed tasks, demands, reservations, file or ingestion facts")
				}
				entries, e := os.ReadDir(f.target)
				if e != nil || len(entries) != 1 || entries[0].Name() != ".pulsemedia_root" {
					t.Fatal("filtered bundle published target", e)
				}
				for _, name := range []string{"Show.S01E01.mkv", "Show.S01E02.mkv"} {
					info, e := os.Stat(filepath.Join(f.source, "Show", name))
					if e != nil || info.Size() != 512 {
						t.Fatal("filtered bundle changed source", e)
					}
				}
			} else {
				if err != nil || receipt == nil {
					t.Fatal("exact minimum rejected full bundle", err)
				}
				highRepo := transferstore.NewRepositoryWithMinimumMediaBytes(f.n.f.a.DBPool(), ids, outbox.New(f.n.f.a.DBPool(), ids, clock.New()), rootfs.NewFilesystem(), minimum+1, transfer.ExecutePreparedPlan)
				replay, err := transfer.NewCompleteTVOrganizationService(highRepo).Confirm(t.Context(), f.n.actor, f.command, "size-boundary")
				if err != nil || replay == nil || *replay != *receipt {
					t.Fatal("higher minimum invalidated original receipt", err)
				}
				if err = f.run(t, receipt); err != nil {
					t.Fatal(err)
				}
				f.assertImported(t, receipt)
			}
			if f.n.adds.Load() != 0 {
				t.Fatal("filtering issued downloader Add")
			}
		})
	}
}
