package subscription

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type requestRepoTest struct {
	row                    ports.MediaRequest
	err                    error
	submits, closes, reads int
}

func (r *requestRepoTest) SubmitMediaRequest(_ context.Context, a *ports.Actor, in mediarequest.Intent, key string) (*ports.MediaRequestReceipt, error) {
	r.submits++
	r.row.Intent = in
	return &ports.MediaRequestReceipt{CommandKey: key, Request: r.row}, r.err
}
func (r *requestRepoTest) ReadMediaRequest(context.Context, *ports.Actor, uuid.UUID) (*ports.MediaRequest, error) {
	r.reads++
	return &r.row, r.err
}
func (r *requestRepoTest) ListMediaRequests(context.Context, *ports.Actor, int, *uuid.UUID) ([]ports.MediaRequest, error) {
	return []ports.MediaRequest{r.row}, r.err
}
func (r *requestRepoTest) ReadMediaRequestReceipt(_ context.Context, _ *ports.Actor, key string) (*ports.MediaRequestReceipt, error) {
	return &ports.MediaRequestReceipt{CommandKey: key, Request: r.row}, r.err
}
func (r *requestRepoTest) CloseMediaRequest(_ context.Context, _ *ports.Actor, _ uuid.UUID, in ports.MediaRequestCloseInput, key string) (*ports.MediaRequestReceipt, error) {
	r.closes++
	state, _ := mediarequest.Transition(r.row.State, in.Action)
	out := r.row
	out.State = state
	out.Version++
	return &ports.MediaRequestReceipt{CommandKey: key, Request: out}, r.err
}

type requestCreatorTest func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error)

func (f requestCreatorTest) Create(c context.Context, a *ports.Actor, i ports.SubscriptionCreationInput, k string) (*ports.SubscriptionCreationReceipt, error) {
	return f(c, a, i, k)
}
func (f requestCreatorTest) Receipt(context.Context, *ports.Actor, string) (*ports.SubscriptionCreationReceipt, error) {
	return nil, ports.ErrUnavailable
}
func requestTestFixture() (*requestRepoTest, *ports.Actor) {
	a := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	return &requestRepoTest{row: ports.MediaRequest{ID: uuid.New(), OwnerID: uuid.MustParse(a.ID), Intent: mediarequest.Intent{MediaType: "tv", TMDBID: 1399, Seasons: []int{2, 7}}, State: mediarequest.Pending, Version: 1}}, a
}
func TestMediaRequestSubmitOnlyIntent(t *testing.T) {
	r, a := requestTestFixture()
	s := NewMediaRequestService(r, nil)
	input := r.row.Intent
	input.Seasons = []int{7, 2}
	out, err := s.Submit(t.Context(), a, input, "original")
	if err != nil || out == nil || r.submits != 1 || !reflect.DeepEqual(out.Request.Intent.Seasons, []int{2, 7}) || !reflect.DeepEqual(input.Seasons, []int{7, 2}) {
		t.Fatal("request publication", err)
	}
	out.Request.Intent.Seasons[0] = 99
	if r.row.Intent.Seasons[0] != 2 {
		t.Fatal("returned request shares repository data")
	}
}
func TestMediaRequestActorAndReadScope(t *testing.T) {
	r, a := requestTestFixture()
	s := NewMediaRequestService(r, nil)
	foreign := *a
	foreign.ID = uuid.NewString()
	if _, err := s.Read(t.Context(), &foreign, r.row.ID); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("foreign record exposed", err)
	}
	for _, token := range [][]string{{}, {"admin"}} {
		actor := *a
		actor.Scopes = token
		if _, err := s.Submit(t.Context(), &actor, r.row.Intent, "key"); !errors.Is(err, ports.ErrForbidden) {
			t.Fatal("token request accepted", err)
		}
	}
	if r.submits != 0 {
		t.Fatal("rejected actor wrote a request")
	}
	a.Role = ports.RoleAdmin
	if _, err := s.Read(t.Context(), a, r.row.ID); err != nil {
		t.Fatal(err)
	}
}
func TestMediaRequestApprovalUsesSavedIntent(t *testing.T) {
	r, _ := requestTestFixture()
	owner := r.row.OwnerID
	a := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	root, client, site := uuid.New(), uuid.New(), uuid.New()
	calls := 0
	s := NewMediaRequestService(r, func(fence ports.MediaRequestApprovalFence) ports.SubscriptionCreator {
		calls++
		if fence.RequestID != r.row.ID || fence.ExpectedVersion != 1 || fence.Key != "approve-original" {
			t.Fatal("approval factory lost original command")
		}
		return requestCreatorTest(func(_ context.Context, who *ports.Actor, in ports.SubscriptionCreationInput, key string) (*ports.SubscriptionCreationReceipt, error) {
			if who.ID != a.ID || *in.UserID != owner || *in.TMDBID != 1399 || in.MediaType != "tv" || !reflect.DeepEqual(in.Seasons, []int{2, 7}) || key != "media.request.approve:"+r.row.ID.String()+":approve-original" {
				t.Fatal("approval changed request owner, identity, scope or key")
			}
			in.Seasons[0] = 99
			return &ports.SubscriptionCreationReceipt{Subscription: ports.SubscriptionRecord{UserID: owner, RootID: root}}, nil
		})
	})
	out, err := s.Approve(t.Context(), a, r.row.ID, ports.MediaRequestApprovalInput{ExpectedVersion: 1, RootID: root, IndexerIDs: []uuid.UUID{site}, DownloaderID: &client}, "approve-original")
	if err != nil || out == nil || calls != 1 || r.row.Intent.Seasons[0] != 2 {
		t.Fatal("approval input isolation", err)
	}
	// An approved row must still reach original receipt recovery in the scoped creator.
	r.row.State = mediarequest.Approved
	r.row.Version = 2
	if _, err = s.Approve(t.Context(), a, r.row.ID, ports.MediaRequestApprovalInput{ExpectedVersion: 1, RootID: root, IndexerIDs: []uuid.UUID{site}, DownloaderID: &client}, "approve-original"); err != nil || calls != 2 {
		t.Fatal("terminal row blocked historical receipt recovery", err)
	}
}
func TestMediaRequestMemberCannotApproveOrReject(t *testing.T) {
	r, a := requestTestFixture()
	factoryCalls := 0
	s := NewMediaRequestService(r, func(ports.MediaRequestApprovalFence) ports.SubscriptionCreator { factoryCalls++; return nil })
	if _, err := s.Approve(t.Context(), a, r.row.ID, ports.MediaRequestApprovalInput{}, "key"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.Close(t.Context(), a, r.row.ID, ports.MediaRequestCloseInput{Action: "reject", ExpectedVersion: 1}, "key"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal(err)
	}
	if factoryCalls+r.closes+r.reads != 0 {
		t.Fatal("member approval reached collaborators")
	}
	if _, err := s.Close(t.Context(), a, r.row.ID, ports.MediaRequestCloseInput{Action: "withdraw", ExpectedVersion: 1}, "withdraw"); err != nil {
		t.Fatal("owner could not withdraw", err)
	}
}
func TestMediaRequestReadFailureAndCancellation(t *testing.T) {
	r, a := requestTestFixture()
	r.err = ports.ErrUnavailable
	s := NewMediaRequestService(r, nil)
	if _, err := s.List(t.Context(), a, 20, nil); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("database failure became empty list", err)
	}
	r.err = nil
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Read(ctx, a, r.row.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read returned a record", err)
	}
	if _, err := s.List(t.Context(), a, 0, nil); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("zero limit accepted", err)
	}
}
