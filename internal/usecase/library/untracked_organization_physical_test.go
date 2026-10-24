package library

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type untrackedOrganizationTestFS struct {
	ports.RootFilesystem
	open func(context.Context, string) (ports.TransferRoot, error)
	lock func(context.Context, ...string) (map[string]ports.TransferRoot, func(), error)
}

func (f *untrackedOrganizationTestFS) OpenRoot(ctx context.Context, path string) (ports.TransferRoot, error) {
	if f.open != nil {
		return f.open(ctx, path)
	}
	return f.RootFilesystem.OpenRoot(ctx, path)
}

func (f *untrackedOrganizationTestFS) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	if f.lock != nil {
		return f.lock(ctx, paths...)
	}
	return f.RootFilesystem.LockRoots(ctx, paths...)
}

func untrackedOrganizationTV(f *untrackedOrganizationTestFixture, single bool) {
	m := &f.repo.capture.material.Confirmation.Material
	m.Scope.TargetMediaType, m.Review.Request.Input.MediaType = "tv", "tv"
	m.Input.CandidateKey = "tmdb.tv:42"
	m.Input.TargetRelativePath = "Example (2020)/Season 01/Example.S01E01E03.mkv"
	m.File.RelativePath = "Example.2020.S01E01E03.mkv"
	date := time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC)
	c := m.Review.Evidence.Candidate
	c.Key, c.Namespace = "tmdb.tv:42", "tmdb.tv"
	c.Units = []ports.UntrackedReviewUnit{{UnitKey: "S01E01", TMDBEpisodeID: 401, Season: 1, Episode: 1, AirDate: &date}, {UnitKey: "S01E03", TMDBEpisodeID: 403, Season: 1, Episode: 3, AirDate: &date}}
	if single {
		c.Units = c.Units[1:]
		m.File.RelativePath = "Example.2020.S01E03.mkv"
		m.Input.TargetRelativePath = "Example (2020)/Season 01/Example.S01E03.mkv"
	}
	f.base.files.relative = m.File.RelativePath
	f.resealMaterial()
}

func TestUntrackedOrganizationMovieAndSparseTVQueueOnly(t *testing.T) {
	for _, kind := range []string{"movie", "tv sparse", "tv single"} {
		for _, action := range []string{"hardlink", "copy", "reflink", "move"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				f := untrackedOrganizationFixture(t)
				if kind != "movie" {
					untrackedOrganizationTV(f, kind == "tv single")
				}
				f.repo.capture.material.Confirmation.Material.Input.Action = action
				f.resealMaterial()
				original := untrackedOrganizationMaterialCopy(f.repo.capture.material)
				f.repo.onCommit = func(_ context.Context, out *ports.UntrackedOrganizationReceipt) {
					if f.base.files.slots.Load() != 2 || f.base.files.releases.Load() != 0 || f.base.files.opens.Load()-f.base.files.closes.Load() != 4 {
						t.Error("commit did not retain both independently opened and locked capabilities")
					}
					out.Operation.CreatedAt = out.Operation.CreatedAt.In(time.FixedZone("historical", 8*3600))
				}
				out, err := f.organize(t.Context())
				if err != nil || out == nil || out.ExecutionAllowed || out.State != "queued" || !untrackedOrganizationOperationEqual(out.Operation, original.Operation) || out.Operation.CreatedAt.Location().String() != "historical" || f.repo.prepares != 1 || f.repo.commits != 1 {
					t.Fatal("frozen organization was not queued exactly once", err)
				}
				if !reflect.DeepEqual(original, f.repo.capture.material) || f.base.files.file != original.Confirmation.Material.File.Identity {
					t.Fatal("queueing mutated frozen intent or source")
				}
				if len(f.base.files.lockOrder) != 2 || f.base.files.lockOrder[0].Device != 5 || f.base.files.lockOrder[1].Device != 6 || f.base.files.locks.Load() != 1 || f.base.files.releases.Load() != 1 {
					t.Fatal("physical slots were not ordered and released once")
				}
				untrackedReviewAssertClosed(t, f.base.files)
				f.assertNoMetadata(t)
			})
		}
	}
}

func TestUntrackedOrganizationPhysicalAndNamespaceChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*untrackedOrganizationTestFixture)
	}{
		{"source file inode", func(f *untrackedOrganizationTestFixture) { f.base.files.file.Inode++ }},
		{"source file device", func(f *untrackedOrganizationTestFixture) { f.base.files.file.Device++ }},
		{"source file size", func(f *untrackedOrganizationTestFixture) { f.base.files.file.Size++ }},
		{"source file mtime", func(f *untrackedOrganizationTestFixture) { f.base.files.file.Mtime++ }},
		{"source root replaced", func(f *untrackedOrganizationTestFixture) {
			i := f.base.files.identities[f.base.files.source]
			i.Inode++
			f.base.files.identities[f.base.files.source] = i
		}},
		{"target root replaced", func(f *untrackedOrganizationTestFixture) {
			i := f.base.files.identities[f.base.files.target]
			i.Inode++
			f.base.files.identities[f.base.files.target] = i
		}},
		{"source root device", func(f *untrackedOrganizationTestFixture) {
			i := f.base.files.identities[f.base.files.source]
			i.Device++
			f.base.files.identities[f.base.files.source] = i
		}},
		{"marker missing", func(f *untrackedOrganizationTestFixture) { f.base.files.markerIdentity = ports.FileIdentity{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			tc.change(f)
			if out, err := f.organize(t.Context()); out != nil || err != ports.ErrUntrackedOrganizationStale || f.repo.commits != 0 {
				t.Fatal("changed physical evidence accepted", err)
			}
			untrackedReviewAssertClosed(t, f.base.files)
		})
	}
	for _, target := range []bool{false, true} {
		name := "source"
		if target {
			name = "target"
		}
		t.Run("displaced held FD/"+name, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			var once sync.Once
			f.base.files.inspect = func(_ context.Context, _, _ string) {
				once.Do(func() {
					path := f.base.files.source
					if target {
						path = f.base.files.target
					}
					i := f.base.files.identities[path]
					i.Inode++
					f.base.files.identities[path] = i
				})
			}
			if out, err := f.organize(t.Context()); out != nil || err != ports.ErrUntrackedOrganizationStale || f.repo.commits != 0 {
				t.Fatal("trusted displaced directory FD", err)
			}
			untrackedReviewAssertClosed(t, f.base.files)
		})
	}
	for _, what := range []string{"file", "marker", "source", "target"} {
		t.Run("last namespace check/"+what, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			fs := f.base.files
			f.service.files = &untrackedOrganizationTestFS{RootFilesystem: fs, open: func(ctx context.Context, path string) (ports.TransferRoot, error) {
				// Two independent opens, two slot opens and the first namespace pass precede this check.
				if fs.opens.Load() == 6 {
					switch what {
					case "file":
						fs.file.Size++
					case "marker":
						fs.markerIdentity.Inode++
					case "source":
						i := fs.identities[fs.source]
						i.Inode++
						fs.identities[fs.source] = i
					case "target":
						i := fs.identities[fs.target]
						i.Inode++
						fs.identities[fs.target] = i
					}
				}
				return fs.OpenRoot(ctx, path)
			}}
			if out, err := f.organize(t.Context()); out != nil || err != ports.ErrUntrackedOrganizationStale || f.repo.commits != 0 {
				t.Fatal("late namespace change committed", err)
			}
			untrackedReviewAssertClosed(t, fs)
		})
	}
	t.Run("directory size and mtime are not identity", func(t *testing.T) {
		f := untrackedOrganizationFixture(t)
		for path, i := range f.base.files.identities {
			i.Size += 999
			i.Mtime += 999
			f.base.files.identities[path] = i
		}
		if out, err := f.organize(t.Context()); err != nil || out == nil || f.repo.commits != 1 {
			t.Fatal("directory churn invalidated physical identity", err)
		}
		untrackedReviewAssertClosed(t, f.base.files)
	})
}

func TestUntrackedOrganizationSamePhysicalRootsAndAliases(t *testing.T) {
	for _, alias := range []bool{false, true} {
		name := "same configured path"
		if alias {
			name = "two configured aliases"
		}
		t.Run(name, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			m := &f.repo.capture.material.Confirmation.Material
			m.Scope.TargetIdentity = m.Scope.SourceIdentity
			fs := f.base.files
			if !alias {
				m.Scope.TargetRootPath = m.Scope.LocalRoot
				delete(fs.identities, fs.target)
				fs.target = fs.source
			} else {
				fs.identities[fs.target] = m.Scope.SourceIdentity
			}
			f.resealMaterial()
			f.repo.onCommit = func(_ context.Context, _ *ports.UntrackedOrganizationReceipt) {
				if fs.slots.Load() != 1 || len(fs.lockOrder) != 1 {
					t.Error("same device/inode locked twice")
				}
			}
			if out, err := f.organize(t.Context()); err != nil || out == nil || f.repo.commits != 1 || fs.locks.Load() != 1 || fs.releases.Load() != 1 {
				t.Fatal("same-root queueing deadlocked or failed", err)
			}
			untrackedReviewAssertClosed(t, fs)
		})
	}
}

func TestUntrackedOrganizationCaptureDriftAndHistoricalInstants(t *testing.T) {
	for _, what := range []string{"operation", "confirmation key", "scope path", "target path", "candidate", "unit", "created", "same instants"} {
		t.Run(what, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			untrackedOrganizationTV(f, false)
			var once sync.Once
			f.base.files.inspect = func(_ context.Context, _, _ string) {
				once.Do(func() {
					m := &f.repo.capture.material
					switch what {
					case "operation":
						m.Operation.StepID = uuid.New()
					case "confirmation key":
						m.Confirmation.CommandKey = "another-key"
					case "scope path":
						m.Confirmation.Material.Scope.LocalRoot = "/other/configured/source"
					case "target path":
						m.Confirmation.Material.Input.TargetRelativePath = "Another/Example.mkv"
					case "candidate":
						m.Confirmation.Material.Review.Evidence.Candidate.Title = "Another"
					case "unit":
						m.Confirmation.Material.Review.Evidence.Candidate.Units[1].TMDBEpisodeID++
					case "created":
						m.Operation.CreatedAt = m.Operation.CreatedAt.Add(time.Second)
					case "same instants":
						zone := time.FixedZone("old offset", 8*3600)
						m.Operation.CreatedAt = m.Operation.CreatedAt.In(zone)
						c := &m.Confirmation.Material
						c.CreatedAt = c.CreatedAt.In(zone)
						c.Scope.ClientVersion = c.Scope.ClientVersion.In(zone)
						c.Scope.CreatedAt = c.Scope.CreatedAt.In(zone)
						c.Scope.UpdatedAt = c.Scope.UpdatedAt.In(zone)
						c.File.FirstObservedAt = c.File.FirstObservedAt.In(zone)
						c.File.LastObservedAt = c.File.LastObservedAt.In(zone)
						c.Review.Request.CreatedAt = c.Review.Request.CreatedAt.In(zone)
						*c.Review.RecordedAt = c.Review.RecordedAt.In(zone)
						for i := range c.Review.Evidence.Candidate.Units {
							p := c.Review.Evidence.Candidate.Units[i].AirDate
							*p = p.In(zone)
						}
					}
				})
			}
			out, err := f.organize(t.Context())
			if what == "same instants" {
				if err != nil || out == nil || f.repo.commits != 1 || out.Operation.CreatedAt.Location().String() != "old offset" {
					t.Fatal("equivalent historical instant changed or rejected", err)
				}
			} else if out != nil || err != ports.ErrUntrackedOrganizationStale || f.repo.commits != 0 {
				t.Fatal("capture changed after validation", err)
			}
			untrackedReviewAssertClosed(t, f.base.files)
		})
	}
}

func TestUntrackedOrganizationCancellationWaitsForActualReturn(t *testing.T) {
	for _, stage := range []string{"open", "lock", "inspect", "namespace", "commit"} {
		t.Run(stage, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			fs := f.base.files
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var gateOnce, blockOnce sync.Once
			var actualReturned atomic.Bool
			unblock := func() { gateOnce.Do(func() { close(release) }) }
			block := func() {
				blockOnce.Do(func() {
					close(entered)
					<-release // Deliberately ignore context, as an in-flight syscall can.
					actualReturned.Store(true)
				})
			}
			switch stage {
			case "open", "namespace":
				f.service.files = &untrackedOrganizationTestFS{RootFilesystem: fs, open: func(ctx context.Context, path string) (ports.TransferRoot, error) {
					root, err := fs.OpenRoot(ctx, path)
					if stage == "open" || fs.opens.Load() == 7 {
						block()
					}
					return root, err
				}}
			case "lock":
				f.service.files = &untrackedOrganizationTestFS{RootFilesystem: fs, lock: func(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
					roots, releaseSlots, err := fs.LockRoots(ctx, paths...)
					block()
					return roots, releaseSlots, err
				}}
			case "inspect":
				fs.inspect = func(_ context.Context, _, _ string) { block() }
			case "commit":
				f.repo.onCommit = func(_ context.Context, _ *ports.UntrackedOrganizationReceipt) { block() }
			}
			var out *ports.UntrackedOrganizationReceipt
			var err error
			go func() { defer close(done); out, err = f.organize(ctx) }()
			t.Cleanup(func() {
				unblock()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("organization call did not actually return after unblocking")
				}
			})
			select {
			case <-entered:
			case <-done:
				t.Fatal("call returned before reaching blocked operation")
			case <-time.After(2 * time.Second):
				t.Fatal("call did not reach blocked operation")
			}
			closes := fs.closes.Load()
			cancel()
			select {
			case <-done:
				t.Fatal("logical cancellation abandoned physical I/O")
			case <-time.After(20 * time.Millisecond):
			}
			if actualReturned.Load() || fs.closes.Load() != closes || fs.releases.Load() != 0 || (stage != "open" && fs.slots.Load() != 2) {
				t.Fatal("cancellation released capabilities or slots before actual return")
			}
			unblock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("call did not return after actual I/O returned")
			}
			if !actualReturned.Load() || out != nil || !errors.Is(err, context.Canceled) {
				t.Fatal("late success escaped cancellation", err)
			}
			wantCommits := 0
			if stage == "commit" {
				wantCommits = 1
			}
			if f.repo.commits != wantCommits {
				t.Fatal("cancellation committed or retried work")
			}
			untrackedReviewAssertClosed(t, fs)
		})
	}
}

func TestUntrackedOrganizationPartialCapabilitiesClose(t *testing.T) {
	for _, stage := range []string{"open error with capability", "lock error with slots", "missing locked source", "namespace offline"} {
		t.Run(stage, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			fs := f.base.files
			want := ports.ErrUnavailable
			switch stage {
			case "open error with capability":
				fs.openError = true
			case "lock error with slots", "missing locked source":
				f.service.files = &untrackedOrganizationTestFS{RootFilesystem: fs, lock: func(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
					roots, release, err := fs.LockRoots(ctx, paths...)
					if err != nil {
						return roots, release, err
					}
					if stage == "lock error with slots" {
						return roots, release, errors.New("private slot failure")
					}
					// Release retains ownership of the original map and its capabilities.
					return map[string]ports.TransferRoot{fs.target: roots[fs.target]}, release, nil
				}}
			case "namespace offline":
				want = ports.ErrUntrackedOrganizationStale
				f.service.files = &untrackedOrganizationTestFS{RootFilesystem: fs, open: func(ctx context.Context, path string) (ports.TransferRoot, error) {
					if fs.opens.Load() == 6 {
						return nil, errors.New("private path offline")
					}
					return fs.OpenRoot(ctx, path)
				}}
			}
			if out, err := f.organize(t.Context()); out != nil || err != want || f.repo.commits != 0 {
				t.Fatal("partial capability failure accepted or leaked", err)
			}
			untrackedReviewAssertClosed(t, fs)
		})
	}
}
