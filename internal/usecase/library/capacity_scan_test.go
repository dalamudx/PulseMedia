package library_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type capacitySamplerFunc func(context.Context, ports.TransferRoot) (ports.RootCapacity, error)

func (f capacitySamplerFunc) SampleCapacity(ctx context.Context, root ports.TransferRoot) (ports.RootCapacity, error) {
	return f(ctx, root)
}

var _ ports.RootCapacitySampler = capacitySamplerFunc(nil)

type capacitySealRecorder struct {
	ports.ScanRepository
	seals chan ports.ScanSeal
}

func (r *capacitySealRecorder) Seal(ctx context.Context, seal ports.ScanSeal) error {
	r.seals <- seal
	return r.ScanRepository.Seal(ctx, seal)
}

type capacityScannerFixture struct {
	scanner *library.Scanner
	repo    *capacitySealRecorder
	pool    *pgxpool.Pool
	root    uuid.UUID
	path    string
}

func newCapacityScannerFixture(t *testing.T) capacityScannerFixture {
	t.Helper()
	_, q, dsn := setupScannerTest(t)
	pool, err := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	path := t.TempDir()
	for _, name := range []string{".pulsemedia_root", "Movie.mkv"} {
		if err = os.WriteFile(filepath.Join(path, name), []byte("capacity fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := q.CreateLibraryRoot(t.Context(), db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Capacity"})
	if err != nil {
		t.Fatal(err)
	}
	repo := &capacitySealRecorder{ScanRepository: postgres.NewScanObservationStore(pool), seals: make(chan ports.ScanSeal, 4)}
	return capacityScannerFixture{scanner: library.NewScanner(idgen.New(), rootfs.NewFilesystem(), repo), repo: repo, pool: pool, root: root.ID, path: path}
}

func (f capacityScannerFixture) publication(t *testing.T, capacity, finalization int, baseline bool) {
	t.Helper()
	var c, n int
	var b bool
	if err := f.pool.QueryRow(t.Context(), `SELECT
	 (SELECT count(*) FROM root_capacity_observation c JOIN scan_run s ON s.id=c.scan_run_id WHERE s.root_id=$1),
	 (SELECT count(*) FROM scan_finalization WHERE root_id=$1),baseline_completed FROM library_root WHERE id=$1`, f.root).Scan(&c, &n, &b); err != nil {
		t.Fatal(err)
	}
	if c != capacity || n != finalization || b != baseline {
		t.Fatalf("capacity=%d finalization=%d baseline=%t; want %d/%d/%t", c, n, b, capacity, finalization, baseline)
	}
}

func TestScannerCapacityOptionalAndFailure(t *testing.T) {
	for _, kind := range []string{"nil", "unavailable", "sample"} {
		t.Run(kind, func(t *testing.T) {
			f := newCapacityScannerFixture(t)
			var calls int
			var sampled ports.RootCapacity
			if kind != "nil" {
				f.scanner.WithCapacitySampler(capacitySamplerFunc(func(ctx context.Context, root ports.TransferRoot) (ports.RootCapacity, error) {
					calls++
					if _, err := root.Identity(ctx); err != nil {
						return ports.RootCapacity{}, err
					}
					sampled = ports.RootCapacity{TotalBytes: 123456789, AvailableBytes: 456789, SampledAt: time.Now()}
					if kind == "unavailable" {
						return sampled, ports.ErrCapacityUnavailable
					}
					return sampled, nil
				}))
			}
			report, err := f.scanner.ScanRoot(t.Context(), f.root)
			if err != nil || report == nil || report.State != "succeeded" || report.FilesObserved != 1 || report.FilesAdded != 1 || report.MissingCandidates != 0 {
				t.Fatalf("optional observation interrupted normal scan: %+v %v", report, err)
			}
			wantCalls, wantRows := 1, 0
			if kind == "nil" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("sampler calls=%d, want %d", calls, wantCalls)
			}
			select {
			case seal := <-f.repo.seals:
				if kind == "sample" {
					wantRows = 1
					if seal.Capacity == nil || *seal.Capacity != sampled {
						t.Fatalf("sample not forwarded intact: %+v", seal.Capacity)
					}
					var exact bool
					if err = f.pool.QueryRow(t.Context(), `SELECT total_bytes=$2::bigint AND available_bytes=$3::bigint AND sampled_at=$4::timestamptz
					 FROM root_capacity_observation WHERE scan_run_id=$1`, report.ScanRunID, sampled.TotalBytes, sampled.AvailableBytes, sampled.SampledAt).Scan(&exact); err != nil || !exact {
						t.Fatalf("sample not persisted intact: %v", err)
					}
				} else if seal.Capacity != nil {
					t.Fatalf("unavailable sample must remain nil, got %+v", seal.Capacity)
				}
			default:
				t.Fatal("normal scan did not seal")
			}
			f.publication(t, wantRows, 1, true)
		})
	}
}

func TestScannerCapacityCancellationRetainsPhysicalSlot(t *testing.T) {
	f := newCapacityScannerFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	entered, unblock := make(chan struct{}), make(chan struct{})
	var unblockOnce sync.Once
	var calls atomic.Int32
	f.scanner.WithCapacitySampler(capacitySamplerFunc(func(_ context.Context, _ ports.TransferRoot) (ports.RootCapacity, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-unblock // Deliberately ignore cancellation like a blocked physical syscall.
		}
		return ports.RootCapacity{TotalBytes: 1000, AvailableBytes: 200, SampledAt: time.Now()}, nil
	}))
	type result struct {
		report *library.ScanReport
		err    error
	}
	done := make(chan result, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		report, err := f.scanner.ScanRoot(ctx, f.root)
		done <- result{report, err}
	}()
	defer func() {
		cancel()
		unblockOnce.Do(func() { close(unblock) })
		<-finished
	}()
	select {
	case <-entered:
	case got := <-done:
		t.Fatalf("scan ended before capacity sampling: %+v", got)
	case <-time.After(10 * time.Second):
		t.Fatal("sampler was never entered")
	}
	cancel()

	// A second scan and a transfer must both remain outside the held physical slot.
	waitCtx, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, err := f.scanner.ScanRoot(waitCtx, f.root)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second scan entered while canceled sampler was still running: %v", err)
	}
	waitCtx, stop = context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, release, err := rootfs.NewFilesystem().LockRoots(waitCtx, f.path)
	stop()
	if err == nil {
		release()
		t.Fatal("cancellation released physical slot before sampler returned")
	}
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("physical exclusion: calls=%d err=%v", calls.Load(), err)
	}
	select {
	case got := <-done:
		t.Fatalf("scan returned while sampler still blocked: %+v", got)
	default:
	}
	f.publication(t, 0, 0, false)
	unblockOnce.Do(func() { close(unblock) })
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) || got.report != nil {
			t.Fatalf("late sample escaped cancellation: %+v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scan failed to finish after physical sampler returned")
	}
	<-finished
	select {
	case seal := <-f.repo.seals:
		t.Fatalf("late sample reached Seal after cancellation: %+v", seal.Capacity)
	default:
	}
	f.publication(t, 0, 0, false)

	recoveryCtx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	report, err := f.scanner.ScanRoot(recoveryCtx, f.root)
	if err != nil || report == nil || report.State != "succeeded" || calls.Load() != 2 {
		t.Fatalf("physical slot did not recover: report=%+v calls=%d err=%v", report, calls.Load(), err)
	}
	f.publication(t, 1, 1, true)
}
