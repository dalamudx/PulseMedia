package seeding_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	seed "github.com/dalamudx/PulseMedia/internal/usecase/seeding"
	"github.com/google/uuid"
)

type executionGate struct {
	snapshot                                                    ports.SeedingExecution
	events                                                      []string
	checks, reserves, prepares, finishes                        int
	closed                                                      atomic.Bool
	checkFailure, reserveFailure, prepareFailure, finishFailure error
	onPrepare                                                   func()
	result                                                      ports.SeedingExecutionResult
	failure                                                     error
}

func (g *executionGate) Snapshot() ports.SeedingExecution { return g.snapshot }
func (g *executionGate) Check(context.Context) error      { g.checks++; return g.checkFailure }
func (g *executionGate) Reserve(context.Context) error {
	g.reserves++
	g.events = append(g.events, "budget")
	return g.reserveFailure
}
func (g *executionGate) Prepare(_ context.Context, o ports.SeedingRemoteTorrent) error {
	g.prepares++
	g.events = append(g.events, "prepare")
	if o.InfoHash != g.snapshot.Target.InfoHash {
		return ports.ErrRejected
	}
	if g.onPrepare != nil {
		g.onPrepare()
	}
	return g.prepareFailure
}
func (g *executionGate) Finish(_ context.Context, result ports.SeedingExecutionResult, failure error) error {
	g.finishes++
	g.events = append(g.events, "finish")
	g.result = result
	g.failure = failure
	return g.finishFailure
}
func (g *executionGate) Close() { g.closed.Store(true) }

type executionRepo struct {
	g     *executionGate
	err   error
	calls int
}

func (r *executionRepo) Acquire(context.Context, ports.JobLease) (ports.SeedingExecutionPermit, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.g, nil
}

type executionNative struct {
	g                                    *executionGate
	observed                             ports.SeedingRemoteTorrent
	constructions, reads, spaces, writes int
	readFailure, spaceFailure            error
	space                                ports.SeedingPathSpace
	outcome                              ports.SeedingRemovalOutcome
	removeFailure                        error
	onFreshRead                          func(context.Context)
	onWrite                              func(context.Context)
	onRecoveryRead                       func(context.Context)
	doubleAuthorize                      bool
}

func (n *executionNative) ReadSeedingTorrent(ctx context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
	n.reads++
	n.g.events = append(n.g.events, "read")
	if hash != n.observed.InfoHash || tag != n.observed.OperationTag {
		return nil, ports.ErrRejected
	}
	if n.onRecoveryRead != nil {
		n.onRecoveryRead(ctx)
	}
	return &n.observed, n.readFailure
}
func (n *executionNative) ReadSeedingFreeSpace(_ context.Context, path string) (ports.SeedingPathSpace, error) {
	n.spaces++
	n.g.events = append(n.g.events, "space")
	if path != n.g.snapshot.Target.SavePath {
		return ports.SeedingPathSpace{}, ports.ErrRejected
	}
	return n.space, n.spaceFailure
}

type executionRemoveFunc func(context.Context) (ports.SeedingRemovalOutcome, error)

func (f executionRemoveFunc) RemoveRetainingFiles(ctx context.Context) (ports.SeedingRemovalOutcome, error) {
	return f(ctx)
}
func (n *executionNative) remover(target ports.SeedingRemovalTarget, a ports.SeedingRemovalAuthorizer) (ports.SeedingRemovalClient, error) {
	n.constructions++
	if target != n.g.snapshot.Target {
		return nil, ports.ErrRejected
	}
	return executionRemoveFunc(func(ctx context.Context) (ports.SeedingRemovalOutcome, error) {
		n.reads++
		n.g.events = append(n.g.events, "fresh-read")
		if n.onFreshRead != nil {
			n.onFreshRead(ctx)
		}
		if n.readFailure != nil {
			return ports.SeedingRemovalNotDispatched, n.readFailure
		}
		if err := a(ctx, n.observed); err != nil {
			return ports.SeedingRemovalNotDispatched, err
		}
		if n.doubleAuthorize {
			if err := a(ctx, n.observed); err == nil {
				return ports.SeedingRemovalUnknown, errors.New("second authorization allowed")
			}
		}
		n.writes++
		n.g.events = append(n.g.events, "write")
		if n.onWrite != nil {
			n.onWrite(ctx)
		}
		return n.outcome, n.removeFailure
	}), nil
}

type executionFactory struct {
	native *executionNative
	calls  int
	err    error
}

func (f *executionFactory) OpenSeedingExecution(_ context.Context, id uuid.UUID, stamp string) (ports.SeedingExecutionNative, error) {
	f.calls++
	if id != f.native.g.snapshot.ClientID || stamp != f.native.g.snapshot.ClientStamp {
		return ports.SeedingExecutionNative{}, ports.ErrRejected
	}
	return ports.SeedingExecutionNative{Reader: f.native, Remover: f.native.remover}, f.err
}
func executionFixture() (*executionRepo, *executionGate, *executionNative, *executionFactory, ports.JobLease) {
	seconds := int64(3600)
	target := ports.SeedingRemovalTarget{InfoHash: strings.Repeat("a", 40), OperationTag: "pulsemedia:op:" + uuid.NewString(), SavePath: "/owned/downloads", TotalBytes: 4096}
	g := &executionGate{snapshot: ports.SeedingExecution{TaskID: uuid.New(), ClientID: uuid.New(), ClientStamp: strings.Repeat("b", 64), Target: target, Rule: rule.Rule{Mode: rule.MatchAll, MinSeedingSeconds: &seconds}, Protection: rule.Protection{Owned: true, Imported: true}}}
	n := &executionNative{g: g, observed: ports.SeedingRemoteTorrent{InfoHash: target.InfoHash, OperationTag: target.OperationTag, SavePath: target.SavePath, TotalBytes: target.TotalBytes, Facts: rule.Observation{Complete: true, SeedingSeconds: &seconds}}, outcome: ports.SeedingRemovalAcknowledged}
	return &executionRepo{g: g}, g, n, &executionFactory{native: n}, ports.JobLease{ID: uuid.New(), Owner: "owned-worker", Token: 1}
}
func TestSeedingRunnerPreparesAfterFreshRead(t *testing.T) {
	repo, g, n, f, lease := executionFixture()
	n.doubleAuthorize = true
	err := seed.NewRunner(repo, f).Execute(t.Context(), lease)
	if err != nil || n.reads != 1 || n.writes != 1 || g.prepares != 1 || g.reserves != 2 || g.result != ports.SeedingExecutionAcknowledged || !g.closed.Load() {
		t.Fatal("single prepare/write", err, g.events)
	}
	if strings.Join(g.events, ",") != "budget,fresh-read,budget,prepare,write,finish" {
		t.Fatal("permit ordering", g.events)
	}
}
func TestSeedingRunnerNoAuthorityNoNetwork(t *testing.T) {
	for _, kind := range []string{"lease", "cancelled", "acquire", "unowned", "not-imported", "pending", "policy", "stamp", "check", "budget", "factory"} {
		t.Run(kind, func(t *testing.T) {
			repo, g, n, f, lease := executionFixture()
			ctx := t.Context()
			switch kind {
			case "lease":
				lease.Token = 0
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "acquire":
				repo.err = ports.ErrForbidden
			case "unowned":
				g.snapshot.Protection.Owned = false
			case "not-imported":
				g.snapshot.Protection.Imported = false
			case "pending":
				g.snapshot.Protection.PendingWork = true
			case "policy":
				g.snapshot.Rule.DeleteFiles = true
			case "stamp":
				g.snapshot.ClientStamp = "bad"
			case "check":
				g.checkFailure = ports.ErrSeedingLeaseLost
			case "budget":
				g.reserveFailure = ports.NewRateLimitError(time.Second, "private")
			case "factory":
				f.err = errors.New("private endpoint")
			}
			err := seed.NewRunner(repo, f).Execute(ctx, lease)
			protected := kind == "unowned" || kind == "not-imported" || kind == "pending"
			if protected {
				if err != nil || g.result != ports.SeedingExecutionIneligible || f.calls != 0 {
					t.Fatal("protected graph", err)
				}
			} else if err == nil {
				t.Fatal("invalid execution accepted")
			}
			if n.reads != 0 || n.writes != 0 || g.prepares != 0 {
				t.Fatal("invalid execution used network")
			}
			if repo.calls > 0 && repo.err == nil && !g.closed.Load() {
				t.Fatal("gate leaked")
			}
		})
	}
}
func TestSeedingRunnerLateAuthorityOrIdentityRejects(t *testing.T) {
	for _, kind := range []string{"permission", "lease", "configuration", "hash", "tag", "path", "size", "incomplete", "threshold", "permit", "after-prepare"} {
		t.Run(kind, func(t *testing.T) {
			repo, g, n, f, lease := executionFixture()
			n.onFreshRead = func(context.Context) {
				switch kind {
				case "permission":
					g.checkFailure = ports.ErrForbidden
				case "lease":
					g.checkFailure = ports.ErrSeedingLeaseLost
				case "configuration":
					g.checkFailure = ports.ErrSeedingConfigConflict
				case "hash":
					n.observed.InfoHash = strings.Repeat("c", 40)
				case "tag":
					n.observed.OperationTag = "pulsemedia:op:" + uuid.NewString()
				case "path":
					n.observed.SavePath = "/changed"
				case "size":
					n.observed.TotalBytes++
				case "incomplete":
					n.observed.Facts.Complete = false
				case "threshold":
					n.observed.Facts.SeedingSeconds = new(int64)
				case "permit":
					g.prepareFailure = ports.ErrSeedingLeaseLost
				case "after-prepare":
					g.onPrepare = func() { g.checkFailure = ports.ErrForbidden }
				}
			}
			err := seed.NewRunner(repo, f).Execute(t.Context(), lease)
			if n.writes != 0 || !g.closed.Load() {
				t.Fatal("late graph dispatched", err)
			}
			if kind == "incomplete" || kind == "threshold" {
				if err != nil || g.result != ports.SeedingExecutionIneligible {
					t.Fatal("criteria changed", err)
				}
			} else if err == nil {
				t.Fatal("late authority accepted")
			}
		})
	}
}
func TestSeedingRunnerFreshCapacity(t *testing.T) {
	for _, kind := range []string{"low", "equal", "different-path", "unknown", "negative", "failure"} {
		t.Run(kind, func(t *testing.T) {
			repo, g, n, f, lease := executionFixture()
			limit, bytes := int64(100), int64(99)
			g.snapshot.Rule = rule.Rule{Mode: rule.MatchAll, LowFreeBytes: &limit}
			n.space = ports.SeedingPathSpace{Path: g.snapshot.Target.SavePath, FreeBytes: &bytes}
			switch kind {
			case "equal":
				bytes = 100
			case "different-path":
				n.space.Path = "/other"
			case "unknown":
				n.space.FreeBytes = nil
			case "negative":
				bytes = -1
			case "failure":
				n.spaceFailure = ports.ErrUnavailable
			}
			err := seed.NewRunner(repo, f).Execute(t.Context(), lease)
			if kind == "low" {
				if err != nil || n.writes != 1 || g.prepares != 1 || g.reserves != 3 {
					t.Fatal("bound low space", err, g.events)
				}
			} else if n.writes != 0 || g.prepares != 0 {
				t.Fatal("capacity authorized removal", err)
			}
			if n.spaces != 1 || n.reads != 1 || !g.closed.Load() {
				t.Fatal("capacity sampling", g.events)
			}
		})
	}
}
func TestSeedingRunnerRecoveryNeverConstructsRemover(t *testing.T) {
	for _, kind := range []string{"absent", "present", "replaced", "unknown", "invalid-current-rule"} {
		t.Run(kind, func(t *testing.T) {
			repo, g, n, f, lease := executionFixture()
			g.snapshot.ObserveOnly = true
			// Recovery authority does not depend on the current rule still being met.
			g.snapshot.Protection = rule.Protection{}
			want := ports.SeedingExecutionPresent
			switch kind {
			case "absent":
				n.readFailure = ports.ErrNotFound
				want = ports.SeedingExecutionAbsent
			case "replaced":
				n.observed.SavePath = "/replaced"
				want = ports.SeedingExecutionUnknown
			case "unknown":
				n.readFailure = ports.ErrUnavailable
				want = ports.SeedingExecutionUnknown
			case "invalid-current-rule":
				g.snapshot.Rule = rule.Rule{}
			}
			err := seed.NewRunner(repo, f).Execute(t.Context(), lease)
			if g.result != want || n.constructions != 0 || n.writes != 0 || g.prepares != 0 || n.reads != 1 || g.reserves != 1 || !g.closed.Load() {
				t.Fatal("recovery entered mutation", err, g.events)
			}
		})
	}
}
func TestSeedingRunnerUnknownAndCommitFailureNeverReplay(t *testing.T) {
	for _, kind := range []string{"lost-receipt", "not-dispatched-after-permit", "commit", "native-failure"} {
		t.Run(kind, func(t *testing.T) {
			repo, g, n, f, lease := executionFixture()
			switch kind {
			case "lost-receipt":
				n.outcome = ports.SeedingRemovalUnknown
				n.removeFailure = errors.New("private endpoint")
			case "not-dispatched-after-permit":
				n.outcome = ports.SeedingRemovalNotDispatched
				n.removeFailure = ports.ErrTimeout
			case "commit":
				g.finishFailure = errors.New("private database")
			case "native-failure":
				n.readFailure = errors.New("private transport")
			}
			err := seed.NewRunner(repo, f).Execute(t.Context(), lease)
			if err == nil || strings.Contains(err.Error(), "private") || n.reads != 1 || g.finishes != 1 || n.writes > 1 || g.prepares > 1 || !g.closed.Load() {
				t.Fatal("unsafe retry/error", err, g.events)
			}
			if kind == "lost-receipt" || kind == "not-dispatched-after-permit" {
				if g.result != ports.SeedingExecutionUnknown {
					t.Fatal("permit released on uncertain send")
				}
			}
		})
	}
}
func TestSeedingRunnerCancellationWaitsForPhysicalIO(t *testing.T) {
	for _, kind := range []string{"fresh-read", "write", "recovery"} {
		t.Run(kind, func(t *testing.T) {
			repo, g, n, f, lease := executionFixture()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			block := func(context.Context) { close(entered); <-release }
			switch kind {
			case "fresh-read":
				n.onFreshRead = block
			case "write":
				n.onWrite = block
			case "recovery":
				g.snapshot.ObserveOnly = true
				n.onRecoveryRead = block
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			go func() { done <- seed.NewRunner(repo, f).Execute(ctx, lease) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				close(release)
				<-done
				t.Fatal("native not entered")
			}
			cancel()
			select {
			case <-done:
				close(release)
				t.Fatal("returned before native I/O")
			case <-time.After(20 * time.Millisecond):
			}
			if g.closed.Load() {
				close(release)
				<-done
				t.Fatal("physical gate released early")
			}
			close(release)
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || !g.closed.Load() || g.finishes != 0 {
					t.Fatal("cancelled result published", err)
				}
			case <-time.After(time.Second):
				t.Fatal("native completion not joined")
			}
			if kind != "write" && n.writes != 0 {
				t.Fatal("cancelled read dispatched")
			}
		})
	}
}
func TestSeedingRunnerRuleSnapshotIsolation(t *testing.T) {
	repo, g, n, f, lease := executionFixture()
	n.onFreshRead = func(context.Context) { *g.snapshot.Rule.MinSeedingSeconds = 7200 }
	// The native observation must not share the snapshot's pointer in this test.
	seconds := int64(3600)
	n.observed.Facts.SeedingSeconds = &seconds
	err := seed.NewRunner(repo, f).Execute(t.Context(), lease)
	if err != nil || n.writes != 1 || g.prepares != 1 {
		t.Fatal("mutable snapshot changed frozen evaluation", err)
	}
}
