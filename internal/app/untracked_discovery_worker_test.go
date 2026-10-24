package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type discoverySchedulerFake struct {
	page     ports.UntrackedDiscoverySourcePage
	result   *ports.UntrackedDiscoveryScheduleResult
	err      error
	requests int
	id       uuid.UUID
	version  int64
}

func (s *discoverySchedulerFake) ListUntrackedDiscoveryScopes(context.Context, string, int) (ports.UntrackedDiscoverySourcePage, error) {
	return s.page, s.err
}
func (s *discoverySchedulerFake) RequestUntrackedDiscovery(_ context.Context, id uuid.UUID, version int64) (*ports.UntrackedDiscoveryScheduleResult, error) {
	s.requests++
	s.id, s.version = id, version
	return s.result, s.err
}
func (s *discoverySchedulerFake) QueueDueUntrackedDiscoveries(context.Context, int) (int, error) {
	return 0, s.err
}
func discoveryWatchScope() ports.UntrackedScope {
	b := watchBinding()
	stamp := time.Now().UTC()
	return ports.UntrackedScope{ID: uuid.New(), OwnerID: uuid.New(), Enabled: true, Version: 1, ClientID: b.ClientID, ClientVersion: b.ClientVersion, ClientStamp: b.ClientStamp, RemotePrefix: b.RemotePrefix, LocalRoot: b.LocalRoot, SourceIdentity: ports.FileIdentity{Device: 1, Inode: 3}, TargetRootID: b.RootID, TargetRootPath: b.RootPath, TargetRootGeneration: b.Generation, TargetMediaType: "movie", TargetMarker: b.MarkerFilename, TargetBaselineScanID: b.BaselineScanID, TargetIdentity: b.RootIdentity, CreatedAt: stamp, UpdatedAt: stamp}
}
func TestUntrackedDiscoveryLegacyWatchJSON(t *testing.T) {
	binding := watchBinding()
	assertAbsent := func(v any) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"ScopeID", "ScopeVersion", "SourceIdentity"} {
			if _, ok := fields[key]; ok {
				t.Fatalf("legacy binding gained frozen evidence field %s", key)
			}
		}
	}
	assertAbsent(binding)
	assertAbsent(watchTarget(binding))
	var round ports.DownloadWatchBinding
	raw, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
	rawRound, err := json.Marshal(round)
	if err != nil || string(raw) != string(rawRound) {
		t.Fatal("historical binding bytes changed", err)
	}
	b := untrackedDiscoveryWatchBinding(discoveryWatchScope())
	raw, err = json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ScopeID", "ScopeVersion", "SourceIdentity"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("scope binding omitted %s", key)
		}
	}
}
func TestUntrackedDiscoveryTargetNamespace(t *testing.T) {
	scope := discoveryWatchScope()
	binding := untrackedDiscoveryWatchBinding(scope)
	target := ports.DownloadWatchTarget{Binding: binding, ScopeID: scope.ID, ScopeVersion: scope.Version, Kind: ports.UntrackedDiscoveryJobKind}
	if downloadWatchTargetIdentity(target) != scope.ID || !validDownloadWatchBinding(binding) {
		t.Fatal("valid scope identity rejected")
	}
	for _, kind := range []string{"operation", "task", "version", "binding", "kind"} {
		t.Run(kind, func(t *testing.T) {
			bad := target
			switch kind {
			case "operation":
				bad.OperationID = uuid.New()
			case "task":
				bad.TaskID = uuid.New()
			case "version":
				bad.ScopeVersion++
			case "binding":
				bad.Binding.ScopeID = uuid.New()
			case "kind":
				bad.Kind = ""
			}
			if downloadWatchTargetIdentity(bad) != uuid.Nil {
				t.Fatal("scope accepted forged download authority")
			}
		})
	}
	ordinary := watchTarget(watchBinding())
	if downloadWatchTargetIdentity(ordinary) != ordinary.OperationID {
		t.Fatal("ordinary target contract changed")
	}
	ordinary.Binding.SourceIdentity = ports.FileIdentity{Device: 1, Inode: 3}
	if validDownloadWatchBinding(ordinary.Binding) {
		t.Fatal("ordinary binding accepted scope-only physical identity")
	}
}
func TestUntrackedDiscoveryHintOnlyQueuesScope(t *testing.T) {
	scope := discoveryWatchScope()
	scheduler := &discoverySchedulerFake{page: ports.UntrackedDiscoverySourcePage{Scopes: []ports.UntrackedScope{scope}, Complete: true, ManifestAt: time.Now().UTC()}, result: &ports.UntrackedDiscoveryScheduleResult{JobID: uuid.New(), Queued: true}}
	repo := &untrackedDiscoveryWatch{scheduler: scheduler}
	page, err := repo.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Limit: 64})
	if err != nil || len(page.Sources) != 1 {
		t.Fatal(page, err)
	}
	targets, err := repo.ResolveDownloadWatchTargets(t.Context(), page.Sources[0].Binding, ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: "Movie.mkv"}, ports.DownloadWatchTargetQuery{Limit: 64})
	if err != nil || len(targets.Targets) != 1 || !targets.Complete {
		t.Fatal(targets, err)
	}
	target := targets.Targets[0]
	if target.TaskID != uuid.Nil || target.OperationID != uuid.Nil {
		t.Fatal("discovery fabricated a download identity")
	}
	wake := make(chan struct{}, 1)
	observer := &untrackedDiscoveryHintObserver{scheduler: scheduler, wake: wake}
	result, err := observer.ObserveDownloadWatchTarget(t.Context(), target)
	if err != nil || result != nil || scheduler.requests != 1 || scheduler.id != scope.ID || scheduler.version != scope.Version || len(wake) != 1 {
		t.Fatal("hint granted completion/organization or lost scope", result, err)
	}
	scheduler.err = ports.ErrUntrackedDiscoveryStale
	if _, err = observer.ObserveDownloadWatchTarget(t.Context(), target); !errors.Is(err, ports.ErrDownloadWatchStale) {
		t.Fatal(err)
	}
	scheduler.err = nil
	scheduler.result = nil
	if _, err = observer.ObserveDownloadWatchTarget(t.Context(), target); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestUntrackedDiscoveryIndependentSourceBaseline(t *testing.T) {
	scope := discoveryWatchScope()
	binding := untrackedDiscoveryWatchBinding(scope)
	root, _, reason := probeDownloadWatchRoot(t.Context(), watchFS(), binding, time.Second)
	if reason != "" || root == nil {
		t.Fatal(reason)
	}
	root.Close()
	binding.SourceIdentity.Inode++
	root, _, reason = probeDownloadWatchRoot(t.Context(), watchFS(), binding, time.Second)
	if reason != "source_identity_invalid" || root != nil {
		t.Fatal("configured independent source was rebound", reason)
	}
	scope.ClientVersion = scope.ClientVersion.In(time.FixedZone("other", 8*60*60))
	scope.CreatedAt = scope.CreatedAt.In(time.FixedZone("other", 8*60*60))
	scope.UpdatedAt = scope.UpdatedAt.In(time.FixedZone("other", 8*60*60))
	original := binding.Stamp
	binding = untrackedDiscoveryWatchBinding(scope)
	if binding.Stamp != original {
		t.Fatal("same timestamp instant changed scope binding")
	}
}
