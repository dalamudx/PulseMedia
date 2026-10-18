package app

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

type upgradeSendExecutorFunc func(context.Context, ports.MovieUpgradeSendJobRequest, uuid.UUID) error

func (f upgradeSendExecutorFunc) ExecuteMovieUpgradeSendJob(c context.Context, r ports.MovieUpgradeSendJobRequest, a uuid.UUID) error {
	return f(c, r, a)
}
func TestMovieUpgradeSendWorkerPayload(t *testing.T) {
	good := fmt.Sprintf(`{"command_id":%q,"operation_id":%q,"task_id":%q,"actor_id":%q}`, uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString())
	for _, raw := range []string{strings.Replace(good, "command_id", "Command_ID", 1), good[:len(good)-1] + `,"command_id":"` + uuid.NewString() + `"}`, strings.Replace(good, "actor_id", "actor_ıd", 1), good + `{}`, good[:len(good)-1] + `,"url":"secret"}`, `[]`, strings.Replace(good, `"command_id":"`, `"command_id":"\n`, 1)} {
		if _, _, _, _, e := movieUpgradeSendPayload([]byte(raw)); e == nil {
			t.Fatal("malformed send identity admitted")
		}
	}
	if _, _, _, _, e := movieUpgradeSendPayload([]byte(good)); e != nil {
		t.Fatal(e)
	}
}
func TestMovieUpgradeSendWorkerDrainsActualReturn(t *testing.T) {
	a, q, _ := observationWorkerFixture()
	q.job.Kind = ports.MovieUpgradeSendJobKind
	q.job.Payload = []byte(fmt.Sprintf(`{"command_id":%q,"operation_id":%q,"task_id":%q,"actor_id":%q}`, uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	exec := upgradeSendExecutorFunc(func(_ context.Context, r ports.MovieUpgradeSendJobRequest, _ uuid.UUID) error {
		if r.Lease.ID != q.job.ID {
			t.Error("wrong lease")
		}
		close(entered)
		<-release
		return nil
	})
	go func() { defer close(done); a.runMovieUpgradeSendJob(ctx, q, exec, &q.job) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor not entered")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("physical call detached on logical cancellation")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not drain")
	}
	if q.failed.Load() != 0 {
		t.Fatal("successful result separately terminated")
	}
}
