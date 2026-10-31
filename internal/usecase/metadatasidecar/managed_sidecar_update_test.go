package metadatasidecar_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatasidecar"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

type managedIDs struct{}

func (managedIDs) NewUUID() uuid.UUID    { return uuid.New() }
func (managedIDs) NewUUIDString() string { return uuid.NewString() }
func updateHash(b []byte) string         { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type managedRepo struct {
	ticket           ports.ManagedSidecarUpdateTicket
	saved, completed int
	validErr         error
	out              ports.ManagedSidecarUpdateResult
	onComplete       func()
	mu               sync.Mutex
	used             bool
}

func (r *managedRepo) LoadManaged(context.Context, *ports.Actor, uuid.UUID, uuid.UUID) (ports.ManagedSidecarUpdateTicket, error) {
	return r.ticket, nil
}
func (r *managedRepo) SaveManagedPreview(_ context.Context, _ *ports.Actor, t ports.ManagedSidecarUpdateTicket, out ports.ManagedSidecarUpdateResult) (ports.ManagedSidecarUpdateResult, error) {
	r.saved++
	r.ticket = t
	r.out = out
	return out, nil
}
func (r *managedRepo) AuthorizeManagedUpdate(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (ports.ManagedSidecarUpdateTicket, ports.ManagedSidecarUpdateResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.used {
		return ports.ManagedSidecarUpdateTicket{}, r.out, nil
	}
	r.used = true
	t := r.ticket
	t.Permit = uuid.New()
	r.out = ports.ManagedSidecarUpdateResult{ID: t.ID, FileID: t.Snapshot.Source.FileID, State: "executing", Artifacts: []ports.ManagedSidecarArtifact{{Name: "movie.nfo", State: "ready"}}}
	return t, r.out, nil
}
func (r *managedRepo) ValidateManagedUpdate(ctx context.Context, _ *ports.Actor, _ ports.ManagedSidecarUpdateTicket) error {
	return errors.Join(ctx.Err(), r.validErr)
}
func (r *managedRepo) CompleteManagedUpdate(_ context.Context, _ *ports.Actor, _ ports.ManagedSidecarUpdateTicket, out ports.ManagedSidecarUpdateResult, _ []ports.ManagedSidecarContent) (ports.ManagedSidecarUpdateResult, error) {
	r.completed++
	if r.onComplete != nil {
		r.onComplete()
	}
	r.out = out
	return out, nil
}
func (r *managedRepo) ReadManagedUpdate(context.Context, *ports.Actor, uuid.UUID, string) (ports.ManagedSidecarUpdateResult, error) {
	return r.out, nil
}

type managedFS struct {
	*filesystem
	observed     ports.ManagedSidecarContent
	replace      func(context.Context, ports.SidecarRequest) (ports.SidecarResult, error)
	replacements int
}

func (f *managedFS) ReadManagedSidecar(context.Context, ports.TransferRoot, ports.SidecarRequest) (ports.ManagedSidecarContent, error) {
	return f.observed, nil
}
func (f *managedFS) ReplaceManagedSidecar(ctx context.Context, _ ports.TransferRoot, req ports.SidecarRequest, _ ports.ManagedSidecarContent, _ uuid.UUID) (ports.SidecarResult, error) {
	f.replacements++
	return f.replace(ctx, req)
}
func managedSetup(t *testing.T) (*fixture, *managedRepo, *managedFS) {
	f := setup(t, movieSnapshot())
	old := ports.ManagedSidecarContent{Name: "movie.nfo", Directory: "Canonical (2024)", Identity: ports.FileIdentity{Device: 10, Inode: 55, Size: 3, Mtime: 123}, Content: []byte("old"), Hash: updateHash([]byte("old"))}
	r := &managedRepo{ticket: ports.ManagedSidecarUpdateTicket{ID: uuid.New(), Snapshot: f.repo.snapshot, Managed: []ports.ManagedSidecarContent{old}, Proposed: []ports.ManagedSidecarContent{{Name: "movie.nfo", Directory: old.Directory, Content: []byte("new"), Hash: updateHash([]byte("new"))}}, ExpiresAt: time.Now().Add(time.Minute)}}
	fs := &managedFS{filesystem: f.fs, observed: old}
	fs.replace = func(context.Context, ports.SidecarRequest) (ports.SidecarResult, error) {
		return ports.SidecarResult{Disposition: ports.SidecarCreated, Published: true, Identity: ports.FileIdentity{Device: 10, Inode: 99, Size: 3, Mtime: 4}}, nil
	}
	return f, r, fs
}
func TestManagedSidecarPreviewTrust(t *testing.T) {
	for _, scenario := range []string{"trusted", "legacy", "changed_bytes", "changed_identity", "forged_hash"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, fs := managedSetup(t)
			switch scenario {
			case "legacy":
				r.ticket.Managed = nil
			case "changed_bytes":
				fs.observed.Content = []byte("manual")
			case "changed_identity":
				fs.observed.Identity.Inode++
			case "forged_hash":
				r.ticket.Managed[0].Hash = updateHash([]byte("other"))
			}
			m := metadata{movie: func(context.Context, int64, string) (*ports.MovieMetadata, error) {
				return &ports.MovieMetadata{MediaType: "movie", TMDBID: 77, Overview: "new provider metadata"}, nil
			}}
			svc := metadatasidecar.NewManagedUpdateService(r, fs, m, nil, managedIDs{})
			out, e := svc.Preview(t.Context(), nil, r.ticket.Snapshot.Source.FileID, r.ticket.Snapshot.Source.UnitID)
			if e != nil {
				t.Fatal(e)
			}
			want := "held"
			if scenario == "trusted" {
				want = "preview"
			}
			if out.State != want || r.saved != 1 || fs.replacements != 0 {
				t.Fatalf("out=%+v saved=%d physical=%d", out, r.saved, fs.replacements)
			}
		})
	}
}
func TestManagedSidecarProviderFailureDoesNotPreview(t *testing.T) {
	_, r, fs := managedSetup(t)
	m := metadata{movie: func(context.Context, int64, string) (*ports.MovieMetadata, error) { return nil, errInjected }}
	out, e := metadatasidecar.NewManagedUpdateService(r, fs, m, nil, managedIDs{}).Preview(t.Context(), nil, r.ticket.Snapshot.Source.FileID, r.ticket.Snapshot.Source.UnitID)
	if !errors.Is(e, errInjected) || r.saved != 0 || out.ID != uuid.Nil {
		t.Fatalf("out=%+v err=%v saved=%d", out, e, r.saved)
	}
}
func TestManagedSidecarCommandFrozenContentAndReceipt(t *testing.T) {
	f, r, fs := managedSetup(t)
	r.onComplete = func() {
		if f.fs.released.Load() {
			t.Error("root slot released before durable writeback")
		}
	}
	fs.replace = func(_ context.Context, req ports.SidecarRequest) (ports.SidecarResult, error) {
		if string(req.Content) != "new" {
			t.Error("frozen bytes changed")
		}
		return ports.SidecarResult{Disposition: ports.SidecarCreated, Published: true, Identity: ports.FileIdentity{Device: 10, Inode: 99, Size: 3}}, nil
	}
	svc := metadatasidecar.NewManagedUpdateService(r, fs, nil, nil, managedIDs{})
	out, e := svc.Command(t.Context(), nil, r.ticket.Snapshot.Source.FileID, r.ticket.ID, "one")
	if e != nil || out.State != "completed" || fs.replacements != 1 || r.completed != 1 || !f.fs.released.Load() {
		t.Fatalf("out=%+v err=%v", out, e)
	}
	_, e = svc.Command(t.Context(), nil, r.ticket.Snapshot.Source.FileID, r.ticket.ID, "one")
	if e != nil || fs.replacements != 1 || r.completed != 1 {
		t.Fatal("receipt replay performed effects")
	}
}
func TestManagedSidecarCommandFencesAndUnknown(t *testing.T) {
	for _, scenario := range []string{"stale", "uncertain"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, fs := managedSetup(t)
			if scenario == "stale" {
				r.validErr = ports.ErrMediaSidecarStale
			} else {
				fs.replace = func(context.Context, ports.SidecarRequest) (ports.SidecarResult, error) {
					return ports.SidecarResult{Disposition: ports.SidecarUncertain, Published: true}, errInjected
				}
			}
			out, e := metadatasidecar.NewManagedUpdateService(r, fs, nil, nil, managedIDs{}).Command(t.Context(), nil, r.ticket.Snapshot.Source.FileID, r.ticket.ID, "key")
			if scenario == "stale" {
				if !errors.Is(e, ports.ErrMediaSidecarStale) || fs.replacements != 0 {
					t.Fatal("stale permission executed")
				}
			} else if e != nil || out.State != "review_required" || r.completed != 1 {
				t.Fatalf("unknown=%+v %v", out, e)
			}
		})
	}
}
func TestManagedSidecarCancellationWaitsForPhysicalCall(t *testing.T) {
	f, r, fs := managedSetup(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	fs.replace = func(context.Context, ports.SidecarRequest) (ports.SidecarResult, error) {
		close(entered)
		<-release
		return ports.SidecarResult{Disposition: ports.SidecarUncertain}, context.Canceled
	}
	svc := metadatasidecar.NewManagedUpdateService(r, fs, nil, nil, managedIDs{})
	go func() { _, e := svc.Command(ctx, nil, r.ticket.Snapshot.Source.FileID, r.ticket.ID, "key"); done <- e }()
	<-entered
	cancel()
	if f.fs.released.Load() {
		t.Fatal("cancellation released physical slot during I/O")
	}
	close(release)
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatalf("err=%v", e)
	}
	if !f.fs.released.Load() || r.completed != 0 {
		t.Fatal("cancellation wrote success or failed to await")
	}
}
