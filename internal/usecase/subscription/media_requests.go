package subscription

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type MediaRequestService struct {
	repo     ports.MediaRequestRepository
	creators ports.MediaRequestApprovalCreatorFactory
}

func NewMediaRequestService(repo ports.MediaRequestRepository, creators ports.MediaRequestApprovalCreatorFactory) *MediaRequestService {
	return &MediaRequestService{repo: repo, creators: creators}
}
func requestActor(a *ports.Actor) (ports.Actor, error) {
	if err := intentActor(a); err != nil {
		return ports.Actor{}, err
	}
	return *a, nil
}
func requestKey(key string) bool { return len(key) <= 80 && validCreationKey(key) }
func requestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, mediarequest.ErrInvalid) {
		return ports.ErrRejected
	}
	for _, allowed := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrRejected, ports.ErrUnavailable, ports.ErrMediaRequestConflict, ports.ErrMediaRequestLimit, ports.ErrInvalidSubscriptionBindings, ports.ErrSubscriptionBindingsConflict, ports.ErrNoSubscriptionRoot, ports.ErrAmbiguousSubscriptionRoot, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, allowed) {
			return allowed
		}
	}
	return ports.ErrUnavailable
}
func requestRecordValid(a ports.Actor, r *ports.MediaRequest) bool {
	if r == nil || r.ID == uuid.Nil || r.OwnerID == uuid.Nil || !a.CanAccessUser(r.OwnerID.String()) || r.Version < 1 {
		return false
	}
	switch r.State {
	case mediarequest.Pending, mediarequest.Approved, mediarequest.Rejected, mediarequest.Withdrawn:
	default:
		return false
	}
	intent, err := mediarequest.CanonicalIntent(r.Intent)
	return err == nil && reflect.DeepEqual(intent, r.Intent)
}
func cloneRequest(r *ports.MediaRequest) *ports.MediaRequest {
	if r == nil {
		return nil
	}
	out := *r
	out.Intent.Seasons = slices.Clone(r.Intent.Seasons)
	if r.DecidedBy != nil {
		v := *r.DecidedBy
		out.DecidedBy = &v
	}
	if r.DecidedAt != nil {
		v := *r.DecidedAt
		out.DecidedAt = &v
	}
	if r.SubscriptionID != nil {
		v := *r.SubscriptionID
		out.SubscriptionID = &v
	}
	return &out
}
func (s *MediaRequestService) Submit(ctx context.Context, actor *ports.Actor, in mediarequest.Intent, key string) (*ports.MediaRequestReceipt, error) {
	a, err := requestActor(actor)
	if err != nil {
		return nil, err
	}
	canonical, err := mediarequest.CanonicalIntent(in)
	if err != nil || !requestKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	private := canonical
	private.Seasons = slices.Clone(canonical.Seasons)
	result, err := s.repo.SubmitMediaRequest(ctx, &a, canonical, key)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if result == nil || result.CommandKey != key || !requestRecordValid(a, &result.Request) || result.Request.OwnerID.String() != a.ID || result.Request.State != mediarequest.Pending || result.Request.Version != 1 || !reflect.DeepEqual(result.Request.Intent, private) {
		return nil, ports.ErrUnavailable
	}
	out := *result
	out.Request = *cloneRequest(&result.Request)
	return &out, nil
}
func (s *MediaRequestService) Read(ctx context.Context, actor *ports.Actor, id uuid.UUID) (*ports.MediaRequest, error) {
	a, err := requestActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	result, err := s.repo.ReadMediaRequest(ctx, &a, id)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if result != nil && !a.CanAccessUser(result.OwnerID.String()) {
		return nil, ports.ErrForbidden
	}
	if !requestRecordValid(a, result) || result.ID != id {
		return nil, ports.ErrUnavailable
	}
	return cloneRequest(result), nil
}
func (s *MediaRequestService) List(ctx context.Context, actor *ports.Actor, limit int, before *uuid.UUID) ([]ports.MediaRequest, error) {
	a, err := requestActor(actor)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 || before != nil && *before == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	if before != nil {
		v := *before
		before = &v
	}
	rows, err := s.repo.ListMediaRequests(ctx, &a, limit, before)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if len(rows) > limit {
		return nil, ports.ErrUnavailable
	}
	out := make([]ports.MediaRequest, len(rows))
	seen := map[uuid.UUID]bool{}
	for i := range rows {
		if !requestRecordValid(a, &rows[i]) || seen[rows[i].ID] {
			return nil, ports.ErrUnavailable
		}
		seen[rows[i].ID] = true
		out[i] = *cloneRequest(&rows[i])
	}
	return out, nil
}
func (s *MediaRequestService) Receipt(ctx context.Context, actor *ports.Actor, key string) (*ports.MediaRequestReceipt, error) {
	a, err := requestActor(actor)
	if err != nil {
		return nil, err
	}
	if !requestKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	result, err := s.repo.ReadMediaRequestReceipt(ctx, &a, key)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if result == nil || result.CommandKey != key || !requestRecordValid(a, &result.Request) {
		return nil, ports.ErrUnavailable
	}
	out := *result
	out.Request = *cloneRequest(&result.Request)
	return &out, nil
}
func (s *MediaRequestService) Close(ctx context.Context, actor *ports.Actor, id uuid.UUID, in ports.MediaRequestCloseInput, key string) (*ports.MediaRequestReceipt, error) {
	a, err := requestActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil || in.ExpectedVersion < 1 || !requestKey(key) || in.Action != "reject" && in.Action != "withdraw" {
		return nil, ports.ErrRejected
	}
	if in.Action == "reject" && a.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	in.Reason, err = mediarequest.CanonicalReason(in.Reason)
	if err != nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	result, err := s.repo.CloseMediaRequest(ctx, &a, id, in, key)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	expected, _ := mediarequest.Transition(mediarequest.Pending, in.Action)
	if result == nil || result.CommandKey != key || !requestRecordValid(a, &result.Request) || result.Request.ID != id || result.Request.State != expected || result.Request.Version != in.ExpectedVersion+1 {
		return nil, ports.ErrUnavailable
	}
	out := *result
	out.Request = *cloneRequest(&result.Request)
	return &out, nil
}
func (s *MediaRequestService) ApprovalReceipt(ctx context.Context, actor *ports.Actor, id uuid.UUID, key string) (*ports.MediaRequestApprovalReceipt, error) {
	a, err := requestActor(actor)
	if err != nil {
		return nil, err
	}
	if a.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || !requestKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil || s.creators == nil {
		return nil, ports.ErrUnavailable
	}
	row, err := s.Read(ctx, &a, id)
	if err != nil {
		return nil, err
	}
	creator := s.creators(ports.MediaRequestApprovalFence{RequestID: id, Key: key})
	if creator == nil {
		return nil, ports.ErrUnavailable
	}
	receipt, err := creator.Receipt(ctx, &a, "media.request.approve:"+id.String()+":"+key)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if receipt == nil || receipt.Subscription.UserID != row.OwnerID || row.SubscriptionID == nil || receipt.Subscription.ID != *row.SubscriptionID {
		return nil, ports.ErrUnavailable
	}
	return &ports.MediaRequestApprovalReceipt{RequestID: id, Subscription: *cloneCreationReceipt(receipt)}, nil
}
func (s *MediaRequestService) Approve(ctx context.Context, actor *ports.Actor, id uuid.UUID, in ports.MediaRequestApprovalInput, key string) (*ports.MediaRequestApprovalReceipt, error) {
	a, err := requestActor(actor)
	if err != nil {
		return nil, err
	}
	if a.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || in.ExpectedVersion < 1 || in.RootID == uuid.Nil || !requestKey(key) || len(in.IndexerIDs) == 0 || len(in.IndexerIDs) > 64 || in.DownloaderID == nil || *in.DownloaderID == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil || s.creators == nil {
		return nil, ports.ErrUnavailable
	}
	row, err := s.Read(ctx, &a, id)
	if err != nil {
		return nil, err
	}
	// The scoped creator decides between original receipt recovery and a live
	// pending version. Do not block recovery by checking today's terminal state.
	owner, tmdb, root, downloader := row.OwnerID, row.Intent.TMDBID, in.RootID, *in.DownloaderID
	input := ports.SubscriptionCreationInput{UserID: &owner, TMDBID: &tmdb, MediaType: row.Intent.MediaType, RootID: &root, QualityPolicyID: in.QualityPolicyID, IndexerIDs: slices.Clone(in.IndexerIDs), DownloaderID: &downloader, ScopeType: "all", Seasons: slices.Clone(row.Intent.Seasons), IncludeSpecials: row.Intent.IncludeSpecials}
	input, err = canonicalCreationInput(input)
	if err != nil {
		return nil, err
	}
	creator := s.creators(ports.MediaRequestApprovalFence{RequestID: id, ExpectedVersion: in.ExpectedVersion, Key: key})
	if creator == nil {
		return nil, ports.ErrUnavailable
	}
	result, err := creator.Create(ctx, &a, input, "media.request.approve:"+id.String()+":"+key)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if result == nil || result.Subscription.UserID != owner || result.Subscription.RootID != root {
		return nil, ports.ErrUnavailable
	}
	return &ports.MediaRequestApprovalReceipt{RequestID: id, Subscription: *cloneCreationReceipt(result)}, nil
}
