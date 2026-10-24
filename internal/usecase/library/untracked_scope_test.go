package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type scopeTestCapture struct{ material ports.UntrackedScopeMaterial }

func (c *scopeTestCapture) UntrackedScopeMaterial() ports.UntrackedScopeMaterial { return c.material }

type scopeTestRepo struct {
	capture                            *scopeTestCapture
	preparation                        *ports.UntrackedScopePreparation
	read                               *ports.UntrackedScope
	receipt                            *ports.UntrackedScopeReceipt
	prepareErr, commitErr, readErr     error
	reads, receipts, prepares, commits int
	evidence                           ports.UntrackedScopeEvidence
	key                                string
	commitHook                         func(*ports.UntrackedScopeReceipt)
}

func (r *scopeTestRepo) ReadUntrackedScope(_ context.Context, _ *ports.Actor, _ uuid.UUID) (*ports.UntrackedScope, error) {
	r.reads++
	return r.read, r.readErr
}

func (r *scopeTestRepo) PrepareUntrackedScope(_ context.Context, a *ports.Actor, _ uuid.UUID, _ ports.UntrackedScopeInput, key string) (*ports.UntrackedScopePreparation, error) {
	r.prepares++
	r.key = key
	a.Name = "repository-local copy"
	return r.preparation, r.prepareErr
}

func (r *scopeTestRepo) CommitUntrackedScope(_ context.Context, c ports.UntrackedScopeCapture, e ports.UntrackedScopeEvidence) (*ports.UntrackedScopeReceipt, error) {
	r.commits++
	r.evidence = e
	if c != r.capture {
		panic("capture was replaced")
	}
	if r.commitErr != nil {
		return nil, r.commitErr
	}
	out := scopeTestReceipt(r.capture.material, e, r.key)
	if r.commitHook != nil {
		r.commitHook(out)
	}
	return out, nil
}

func (r *scopeTestRepo) ReadUntrackedScopeReceipt(_ context.Context, _ *ports.Actor, _ uuid.UUID, _ string) (*ports.UntrackedScopeReceipt, error) {
	r.receipts++
	return r.receipt, r.readErr
}

type scopeTestFS struct {
	t                 *testing.T
	repo              *scopeTestRepo
	paths             []string
	closed, inspected int
	identities        []ports.FileIdentity
	markers           []ports.FileIdentity
	failStage         string
	failOpen          int
	hook              func(string)
}

type scopeTestRoot struct {
	fs     *scopeTestFS
	index  int
	closed bool
}

func (f *scopeTestFS) OpenRoot(_ context.Context, p string) (ports.TransferRoot, error) {
	if f.repo.prepares != 1 {
		f.t.Fatal("filesystem accessed before exactly one Prepare")
	}
	f.paths = append(f.paths, p)
	i := len(f.paths) - 1
	if i >= len(f.identities) {
		f.t.Fatal("unexpected additional OpenRoot")
	}
	if f.hook != nil {
		f.hook("open")
	}
	r := &scopeTestRoot{fs: f, index: i}
	if f.failStage == "open" && i == f.failOpen {
		return r, errors.New("secret /configured/path")
	}
	return r, nil
}

func (f *scopeTestFS) LockRoots(context.Context, ...string) (map[string]ports.TransferRoot, func(), error) {
	f.t.Fatal("scope configuration must not lock roots")
	return nil, nil, ports.ErrUnavailable
}

func (r *scopeTestRoot) Identity(context.Context) (ports.FileIdentity, error) {
	if r.closed {
		r.fs.t.Fatal("identity after close")
	}
	if r.fs.hook != nil {
		r.fs.hook("identity")
	}
	if r.fs.failStage == "identity" {
		return ports.FileIdentity{}, errors.New("secret identity error")
	}
	return r.fs.identities[r.index], nil
}

func (r *scopeTestRoot) Inspect(_ context.Context, marker string) (ports.FileIdentity, error) {
	if r.closed {
		r.fs.t.Fatal("inspect after close")
	}
	if marker != ".pulsemedia_root" {
		r.fs.t.Fatalf("unexpected marker %q", marker)
	}
	r.fs.inspected++
	if r.fs.hook != nil {
		r.fs.hook("inspect")
	}
	if r.fs.failStage == "inspect" {
		return ports.FileIdentity{}, errors.New("secret marker error")
	}
	return r.fs.markers[r.index], nil
}

func (r *scopeTestRoot) Close() {
	if r.closed {
		r.fs.t.Fatal("double close")
	}
	r.closed = true
	r.fs.closed++
}

func scopeTestReceipt(m ports.UntrackedScopeMaterial, e ports.UntrackedScopeEvidence, key string) *ports.UntrackedScopeReceipt {
	stamp := time.Date(2026, 10, 21, 12, 34, 56, 123456000, time.FixedZone("original", 3600))
	source := e.SourceIdentity
	if !m.Enabled {
		source = untrackedScopePhysical(m.SourceIdentity)
	}
	return &ports.UntrackedScopeReceipt{CommandKey: key, Scope: ports.UntrackedScope{
		ID: m.ScopeID, OwnerID: m.OwnerID, Enabled: m.Enabled, Version: m.ExpectedVersion + 1,
		ClientID: m.ClientID, ClientVersion: m.ClientVersion, ClientStamp: m.ClientStamp,
		RemotePrefix: m.RemotePrefix, LocalRoot: m.LocalRoot, SourceIdentity: source,
		TargetRootID: m.TargetRoot.ID, TargetRootPath: m.TargetRoot.Path,
		TargetRootGeneration: m.TargetRoot.Generation, TargetMediaType: m.TargetRoot.MediaType,
		TargetMarker: m.TargetRoot.MarkerFilename, TargetBaselineScanID: m.TargetBaselineScanID,
		TargetIdentity: untrackedScopePhysical(m.TargetIdentity), CreatedAt: stamp, UpdatedAt: stamp,
	}}
}

func scopeTestFixture(t *testing.T) (*UntrackedScopeService, *scopeTestRepo, *scopeTestFS, *ports.Actor, ports.UntrackedScopeInput) {
	t.Helper()
	owner := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	m := ports.UntrackedScopeMaterial{
		ScopeID: uuid.MustParse("20000000-0000-4000-8000-000000000002"), OwnerID: owner,
		ExpectedVersion: 3, Enabled: true,
		ClientID:      uuid.MustParse("30000000-0000-4000-8000-000000000003"),
		ClientVersion: time.Date(2026, 10, 20, 1, 2, 3, 4000, time.UTC), ClientStamp: "frozen-config",
		RemotePrefix: "/remote/downloads", LocalRoot: "/configured/source",
		TargetRoot:           ports.ScanRootSnapshot{ID: uuid.MustParse("40000000-0000-4000-8000-000000000004"), Path: "/configured/library", Generation: 2, MediaType: "movie", MarkerFilename: ".pulsemedia_root"},
		TargetBaselineScanID: uuid.MustParse("50000000-0000-4000-8000-000000000005"),
		TargetIdentity:       ports.FileIdentity{Device: 7, Inode: 20, Size: 1024, Mtime: 100},
	}
	capture := &scopeTestCapture{material: m}
	r := &scopeTestRepo{capture: capture, preparation: &ports.UntrackedScopePreparation{Capture: capture}}
	f := &scopeTestFS{t: t, repo: r,
		identities: []ports.FileIdentity{{Device: 6, Inode: 10, Size: 200, Mtime: 200}, {Device: 7, Inode: 20, Size: 400, Mtime: 300}, {Device: 6, Inode: 10, Size: 500, Mtime: 400}, {Device: 7, Inode: 20, Size: 600, Mtime: 500}},
		markers:    []ports.FileIdentity{{}, {Device: 7, Inode: 30, Size: 1, Mtime: 50}, {}, {Device: 7, Inode: 30, Size: 1, Mtime: 50}},
	}
	in := ports.UntrackedScopeInput{Enabled: true, ExpectedVersion: 3, ClientID: m.ClientID, ExpectedClientVersion: m.ClientVersion, RemotePrefix: m.RemotePrefix, TargetRootID: m.TargetRoot.ID, ExpectedRootGeneration: 2}
	return NewUntrackedScopeService(r, f), r, f, &ports.Actor{Type: ports.ActorTypeUser, ID: owner.String(), Role: ports.RoleAdmin, Name: "caller"}, in
}

func scopeTestSave(s *UntrackedScopeService, r *scopeTestRepo, a *ports.Actor, in ports.UntrackedScopeInput) (*ports.UntrackedScopeReceipt, error) {
	return s.Save(context.Background(), a, r.capture.material.ScopeID, in, "scope:key-1")
}

func TestUntrackedScopeReadOnly(t *testing.T) {
	s, r, f, a, _ := scopeTestFixture(t)
	id := r.capture.material.ScopeID
	if got, err := s.Read(context.Background(), a, id); err != nil || got != nil {
		t.Fatalf("nil read: %v %v", got, err)
	}
	r.receipt = scopeTestReceipt(r.capture.material, ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 6, Inode: 10}, TargetIdentity: ports.FileIdentity{Device: 7, Inode: 20}}, "scope:key-1")
	r.read = &r.receipt.Scope
	got, err := s.Read(context.Background(), a, id)
	if err != nil || got == r.read || !reflect.DeepEqual(*got, *r.read) {
		t.Fatalf("read: %v %v", got, err)
	}
	receipt, err := s.Receipt(context.Background(), a, id, "scope:key-1")
	if err != nil || receipt == r.receipt || !reflect.DeepEqual(receipt, r.receipt) {
		t.Fatalf("receipt: %v %v", receipt, err)
	}
	r.receipt = nil
	if _, err := s.Receipt(context.Background(), a, id, "scope:key-1"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
	if len(f.paths) != 0 || r.prepares != 0 || r.commits != 0 {
		t.Fatal("read performed work")
	}
}

func TestUntrackedScopeReplayPreservesReceipt(t *testing.T) {
	s, r, f, a, in := scopeTestFixture(t)
	r.preparation = &ports.UntrackedScopePreparation{Receipt: scopeTestReceipt(r.capture.material, ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 6, Inode: 10}, TargetIdentity: ports.FileIdentity{Device: 7, Inode: 20}}, "scope:key-1")}
	before, err := json.Marshal(r.preparation.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	got, err := scopeTestSave(s, r, a, in)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || got == r.preparation.Receipt || !reflect.DeepEqual(got, r.preparation.Receipt) {
		t.Fatal("historical receipt changed or shared")
	}
	got.Scope.ClientStamp = "caller mutation"
	if r.preparation.Receipt.Scope.ClientStamp != "frozen-config" || len(f.paths) != 0 || r.commits != 0 {
		t.Fatal("replay mutated or executed")
	}
}

func TestUntrackedScopeDisabledNeedsNoFilesystem(t *testing.T) {
	s, r, f, a, in := scopeTestFixture(t)
	in.Enabled = false
	r.capture.material.Enabled = false
	r.capture.material.SourceIdentity = ports.FileIdentity{Device: 6, Inode: 10, Size: 999, Mtime: 123}
	s.files = nil
	got, err := scopeTestSave(s, r, a, in)
	if err != nil || got.Scope.Enabled || got.Scope.SourceIdentity != (ports.FileIdentity{Device: 6, Inode: 10}) || r.commits != 1 || r.evidence != (ports.UntrackedScopeEvidence{}) || len(f.paths) != 0 {
		t.Fatalf("disabled: %v %v", got, err)
	}
}

func TestUntrackedScopeAuthorizationAndInputBeforeRepository(t *testing.T) {
	for _, name := range []string{"nil", "member", "token", "empty-token", "noncanonical", "negative-version", "max-version", "client", "client-version", "target", "generation", "relative", "empty-prefix", "unclean-prefix", "nul-prefix", "bad-key", "long-key", "canceled"} {
		t.Run(name, func(t *testing.T) {
			s, r, f, a, in := scopeTestFixture(t)
			ctx := context.Background()
			key := "scope:key-1"
			want := ports.ErrRejected
			switch name {
			case "nil":
				a = nil
				want = ports.ErrUnauthorized
			case "member":
				a.Role = "member"
				want = ports.ErrForbidden
			case "token":
				a.Scopes = []string{"admin"}
				want = ports.ErrForbidden
			case "empty-token":
				a.Scopes = []string{}
				want = ports.ErrForbidden
			case "noncanonical":
				a.ID = "{" + a.ID + "}"
				want = ports.ErrForbidden
			case "negative-version":
				in.ExpectedVersion = -1
			case "max-version":
				in.ExpectedVersion = math.MaxInt64
			case "client":
				in.ClientID = uuid.Nil
			case "client-version":
				in.ExpectedClientVersion = time.Time{}
			case "target":
				in.TargetRootID = uuid.Nil
			case "generation":
				in.ExpectedRootGeneration = 0
			case "relative":
				in.RemotePrefix = "remote"
			case "empty-prefix":
				in.RemotePrefix = ""
			case "unclean-prefix":
				in.RemotePrefix = "/remote/../other"
			case "nul-prefix":
				in.RemotePrefix = "/remote\x00"
			case "bad-key":
				key = "secret key"
			case "long-key":
				key = strings.Repeat("a", 161)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				a = nil
				want = context.Canceled
			}
			if _, err := s.Save(ctx, a, r.capture.material.ScopeID, in, key); !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			if r.prepares != 0 || r.commits != 0 || len(f.paths) != 0 {
				t.Fatal("invalid request reached dependencies")
			}
		})
	}
}

func TestUntrackedScopeInvalidCaptureBeforeFilesystem(t *testing.T) {
	for _, name := range []string{"scope", "owner", "version", "enabled", "client-version", "stamp", "source", "target", "target-id", "generation", "media", "marker", "baseline", "identity", "both", "neither", "nil"} {
		t.Run(name, func(t *testing.T) {
			s, r, f, a, in := scopeTestFixture(t)
			m := &r.capture.material
			switch name {
			case "scope":
				m.ScopeID = uuid.Nil
			case "owner":
				m.OwnerID = uuid.Nil
			case "version":
				m.ExpectedVersion++
			case "enabled":
				m.Enabled = false
			case "client-version":
				m.ClientVersion = m.ClientVersion.Add(time.Nanosecond)
			case "stamp":
				m.ClientStamp = ""
			case "source":
				m.LocalRoot = "/configured/../source"
			case "target":
				m.TargetRoot.Path = "relative"
			case "target-id":
				m.TargetRoot.ID = uuid.Nil
			case "generation":
				m.TargetRoot.Generation++
			case "media":
				m.TargetRoot.MediaType = "other"
			case "marker":
				m.TargetRoot.MarkerFilename = "../marker"
			case "baseline":
				m.TargetBaselineScanID = uuid.Nil
			case "identity":
				m.TargetIdentity.Inode = 0
			case "both":
				r.preparation.Receipt = &ports.UntrackedScopeReceipt{}
			case "neither":
				r.preparation = &ports.UntrackedScopePreparation{}
			case "nil":
				r.preparation = nil
			}
			id := uuid.MustParse("20000000-0000-4000-8000-000000000002")
			if _, err := s.Save(context.Background(), a, id, in, "scope:key-1"); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal(err)
			}
			if r.prepares != 1 || r.commits != 0 || len(f.paths) != 0 {
				t.Fatal("invalid capture authorized I/O")
			}
		})
	}
}

func TestUntrackedScopeIndependentBaselinesAndDirectoryChurn(t *testing.T) {
	s, r, f, a, in := scopeTestFixture(t)
	in.ExpectedClientVersion = in.ExpectedClientVersion.In(time.FixedZone("other", -3600))
	got, err := scopeTestSave(s, r, a, in)
	if err != nil {
		t.Fatal(err)
	}
	want := ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 6, Inode: 10}, TargetIdentity: ports.FileIdentity{Device: 7, Inode: 20}}
	if r.evidence != want || got.Scope.SourceIdentity != want.SourceIdentity || got.Scope.TargetIdentity != want.TargetIdentity || got.ExecutionAllowed {
		t.Fatal("identities not independently frozen")
	}
	if !reflect.DeepEqual(f.paths, []string{"/configured/source", "/configured/library", "/configured/source", "/configured/library"}) || f.closed != 4 || f.inspected != 2 {
		t.Fatalf("probe lifecycle: %#v closed=%d inspected=%d", f.paths, f.closed, f.inspected)
	}
	if a.Name != "caller" {
		t.Fatal("actor shared with repository")
	}
}

func TestUntrackedScopeRejectsReplacement(t *testing.T) {
	for _, name := range []string{"baseline", "source-reopen", "target-reopen", "marker-reopen"} {
		t.Run(name, func(t *testing.T) {
			s, r, f, a, in := scopeTestFixture(t)
			switch name {
			case "baseline":
				r.capture.material.TargetIdentity.Inode++
			case "source-reopen":
				f.identities[2].Inode++
			case "target-reopen":
				f.identities[3].Inode++
			case "marker-reopen":
				f.markers[3].Inode++
			}
			if _, err := scopeTestSave(s, r, a, in); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal(err)
			}
			if r.commits != 0 || f.closed != len(f.paths) {
				t.Fatal("replacement committed or leaked capability")
			}
		})
	}
}

func TestUntrackedScopeProbeErrorsCloseCapabilities(t *testing.T) {
	for _, stage := range []string{"open", "identity", "inspect"} {
		t.Run(stage, func(t *testing.T) {
			s, r, f, a, in := scopeTestFixture(t)
			f.failStage = stage
			if _, err := scopeTestSave(s, r, a, in); err != ports.ErrUnavailable {
				t.Fatalf("unsafe error: %v", err)
			}
			if r.commits != 0 || f.closed != len(f.paths) {
				t.Fatal("failed probe committed or leaked")
			}
		})
	}
}

func TestUntrackedScopeCancellationAfterActualProbeReturn(t *testing.T) {
	for _, stage := range []string{"open", "identity", "inspect"} {
		t.Run(stage, func(t *testing.T) {
			s, r, f, a, in := scopeTestFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			returned := false
			f.hook = func(at string) {
				if at != stage {
					return
				}
				cancel()
				if r.commits != 0 || f.closed != len(f.paths)-1 {
					t.Fatal("released active capability before I/O return")
				}
				returned = true
			}
			_, err := s.Save(ctx, a, r.capture.material.ScopeID, in, "scope:key-1")
			if !errors.Is(err, context.Canceled) || !returned || r.commits != 0 || f.closed != len(f.paths) {
				t.Fatalf("cancellation lifecycle: returned=%v error=%v", returned, err)
			}
		})
	}
}

func TestUntrackedScopeCommitRejectionAndUnknownDoNotRetry(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			s, r, f, a, in := scopeTestFixture(t)
			want := ports.ErrEndpointVersionConflict
			r.commitErr = fmt.Errorf("private DB detail: %w", want)
			if unknown {
				r.commitErr = errors.New("lost commit acknowledgement with secret DSN")
				want = ports.ErrUnavailable
			}
			if _, err := scopeTestSave(s, r, a, in); err != want {
				t.Fatalf("got %v want %v", err, want)
			}
			if r.prepares != 1 || r.commits != 1 || f.closed != 4 {
				t.Fatal("retried unknown result or leaked")
			}
		})
	}
}

func TestUntrackedScopeRejectsForgedReceipts(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, field := range []string{"execution", "key", "id", "owner", "version", "enabled", "client", "client-version", "prefix", "root", "generation", "stamp", "source-path", "target-path", "media", "marker", "baseline", "source-identity", "target-identity"} {
			t.Run(fmt.Sprintf("replay=%v/%s", replay, field), func(t *testing.T) {
				s, r, f, a, in := scopeTestFixture(t)
				mutate := func(receipt *ports.UntrackedScopeReceipt) {
					c := &receipt.Scope
					switch field {
					case "execution":
						receipt.ExecutionAllowed = true
					case "key":
						receipt.CommandKey = "another"
					case "id":
						c.ID = uuid.Nil
					case "owner":
						c.OwnerID = uuid.Nil
					case "version":
						c.Version++
					case "enabled":
						c.Enabled = false
					case "client":
						c.ClientID = uuid.Nil
					case "client-version":
						c.ClientVersion = c.ClientVersion.Add(time.Second)
					case "prefix":
						c.RemotePrefix = "/other"
					case "root":
						c.TargetRootID = uuid.Nil
					case "generation":
						c.TargetRootGeneration++
					case "stamp":
						c.ClientStamp = ""
					case "source-path":
						c.LocalRoot = "relative"
					case "target-path":
						c.TargetRootPath = "relative"
					case "media":
						c.TargetMediaType = "invalid"
					case "marker":
						c.TargetMarker = "../marker"
					case "baseline":
						c.TargetBaselineScanID = uuid.Nil
					case "source-identity":
						c.SourceIdentity.Inode = 0
					case "target-identity":
						c.TargetIdentity.Inode = 0
					}
				}
				if replay {
					receipt := scopeTestReceipt(r.capture.material, ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 6, Inode: 10}, TargetIdentity: ports.FileIdentity{Device: 7, Inode: 20}}, "scope:key-1")
					mutate(receipt)
					r.preparation = &ports.UntrackedScopePreparation{Receipt: receipt}
				} else {
					r.commitHook = mutate
				}
				if _, err := scopeTestSave(s, r, a, in); !errors.Is(err, ports.ErrUnavailable) {
					t.Fatal(err)
				}
				if replay && (r.commits != 0 || len(f.paths) != 0) {
					t.Fatal("forged replay executed")
				}
			})
		}
	}
}

func TestUntrackedScopeUnavailableDependenciesAndPrepareFailure(t *testing.T) {
	for _, name := range []string{"service", "repo", "files", "prepare"} {
		t.Run(name, func(t *testing.T) {
			s, r, f, a, in := scopeTestFixture(t)
			switch name {
			case "service":
				s = nil
			case "repo":
				s.repo = nil
			case "files":
				s.files = nil
			case "prepare":
				r.prepareErr = errors.New("private DB failure")
			}
			if _, err := scopeTestSave(s, r, a, in); err != ports.ErrUnavailable {
				t.Fatal(err)
			}
			if r.commits != 0 || len(f.paths) != 0 {
				t.Fatal("unavailable dependency authorized work")
			}
		})
	}
}
