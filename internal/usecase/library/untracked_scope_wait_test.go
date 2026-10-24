package library

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestUntrackedScopeCancellationWaitsActualProbe(t *testing.T) {
	service, repo, files, actor, input := scopeTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	releaseIO := func() { releaseOnce.Do(func() { close(release) }) }
	files.hook = func(stage string) {
		if stage == "open" {
			enteredOnce.Do(func() { close(entered); <-release })
		}
	}
	result := make(chan error, 1)
	finished := false
	t.Cleanup(func() {
		cancel()
		releaseIO()
		if !finished {
			<-result
		}
	})
	go func() {
		_, err := service.Save(ctx, actor, repo.capture.material.ScopeID, input, "cancel-during-root-probe")
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	cancel()
	select {
	case err := <-result:
		finished = true
		t.Fatalf("returned before physical probe ended: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	releaseIO()
	select {
	case err := <-result:
		finished = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("probe did not drain after release")
	}
	if repo.commits != 0 || len(files.paths) != 1 || files.closed != 1 {
		t.Fatalf("late probe escaped cleanup: commits=%d opened=%d closed=%d", repo.commits, len(files.paths), files.closed)
	}
}

func TestUntrackedScopeReadRejectsDifferentOwner(t *testing.T) {
	service, repo, files, actor, _ := scopeTestFixture(t)
	evidence := ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 1, Inode: 2}, TargetIdentity: repo.capture.material.TargetIdentity}
	config := scopeTestReceipt(repo.capture.material, evidence, "old-key").Scope
	config.OwnerID = uuid.MustParse("70000000-0000-4000-8000-000000000007")
	repo.read = &config
	out, err := service.Read(context.Background(), actor, config.ID)
	if !errors.Is(err, ports.ErrUnavailable) || out != nil || len(files.paths) != 0 {
		t.Fatal("different owner's safe paths became visible")
	}
}
