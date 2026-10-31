package transfer

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type executionPreparer func(context.Context, uuid.UUID, ...ports.JobLease) (*ports.PreparedTransfer, error)

func (f executionPreparer) PrepareTransferExecution(c context.Context, id uuid.UUID, l ...ports.JobLease) (*ports.PreparedTransfer, error) {
	return f(c, id, l...)
}
func TestTransferCoordinatorOrderingAndFaults(t *testing.T) {
	failure := errors.New("storage failure")
	for _, tc := range []struct {
		name                          string
		completed, review, ineligible bool
		stepErr, finishErr            error
		want                          []string
		err                           error
	}{
		{name: "success", want: []string{"prepare", "step", "finish", "release"}},
		{name: "completed replay", completed: true, want: []string{"prepare", "finish", "release"}},
		{name: "marker review", review: true, want: []string{"prepare", "review", "release"}, err: ports.ErrTransferReviewRequired},
		{name: "media filter", ineligible: true, want: []string{"prepare", "review", "release"}, err: ports.ErrTransferReviewRequired},
		{name: "physical uncertainty", stepErr: ports.ErrTransferReviewRequired, want: []string{"prepare", "step", "review", "release"}, err: ports.ErrTransferReviewRequired},
		{name: "step storage failure", stepErr: failure, want: []string{"prepare", "step", "release"}, err: failure},
		{name: "receipt failure", finishErr: failure, want: []string{"prepare", "step", "finish", "release"}, err: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := []string{}
			id := uuid.New()
			lease := ports.JobLease{ID: uuid.New(), Owner: "owner", Token: 7}
			prepare := executionPreparer(func(c context.Context, got uuid.UUID, leases ...ports.JobLease) (*ports.PreparedTransfer, error) {
				if c != t.Context() || got != id || !reflect.DeepEqual(leases, []ports.JobLease{lease}) {
					t.Fatal("authority changed")
				}
				calls = append(calls, "prepare")
				return &ports.PreparedTransfer{Completed: tc.completed, ReviewRequired: tc.review, Release: func() { calls = append(calls, "release") }, Review: func(context.Context) error { calls = append(calls, "review"); return ports.ErrTransferReviewRequired }, Complete: func(context.Context) error { calls = append(calls, "finish"); return tc.finishErr }, Steps: []ports.TransferExecutionStep{{State: "completed", Execute: func(context.Context) error { t.Fatal("completed step repeated"); return nil }}, {State: "pending", Eligible: !tc.ineligible, Execute: func(context.Context) error { calls = append(calls, "step"); return tc.stepErr }}, {State: "completed"}}}, nil
			})
			err := ExecutePreparedPlan(t.Context(), prepare, id, lease)
			if !errors.Is(err, tc.err) || !reflect.DeepEqual(calls, tc.want) {
				t.Fatalf("calls=%v error=%v", calls, err)
			}
		})
	}
}
func TestTransferCoordinatorCancellationRetainsPhysicalOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, exit, released := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	prepare := executionPreparer(func(context.Context, uuid.UUID, ...ports.JobLease) (*ports.PreparedTransfer, error) {
		return &ports.PreparedTransfer{Release: func() { close(released) }, Review: func(context.Context) error { t.Error("cancelled syscall classified as review"); return nil }, Complete: func(context.Context) error { t.Error("cancelled syscall finished plan"); return nil }, Steps: []ports.TransferExecutionStep{{State: "executing", Execute: func(context.Context) error { close(entered); <-exit; return ports.ErrTransferReviewRequired }}}}, nil
	})
	go func() { finished <- ExecutePreparedPlan(ctx, prepare, uuid.New()) }()
	<-entered
	cancel()
	select {
	case <-released:
		t.Error("physical slot released on logical cancellation")
	default:
	}
	close(exit)
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-released
}
func TestTransferCoordinatorRejectsIncompletePreparation(t *testing.T) {
	for _, attempt := range []*ports.PreparedTransfer{nil, {}, {Complete: func(context.Context) error { return nil }}} {
		prepare := executionPreparer(func(context.Context, uuid.UUID, ...ports.JobLease) (*ports.PreparedTransfer, error) {
			return attempt, nil
		})
		if err := ExecutePreparedPlan(t.Context(), prepare, uuid.New()); !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(err)
		}
	}
}
