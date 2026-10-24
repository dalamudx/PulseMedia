package subscription

import (
	"context"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type requestReceiptReaderTest struct {
	requestCreatorTest
	read func(context.Context, *ports.Actor, string) (*ports.SubscriptionCreationReceipt, error)
}

func (r requestReceiptReaderTest) Receipt(ctx context.Context, a *ports.Actor, key string) (*ports.SubscriptionCreationReceipt, error) {
	return r.read(ctx, a, key)
}

func TestMediaRequestApprovalReceiptReadOnly(t *testing.T) {
	r, _ := requestTestFixture()
	admin := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	sid := uuid.New()
	r.row.State = mediarequest.Approved
	r.row.Version = 2
	r.row.SubscriptionID = &sid
	original := &ports.SubscriptionCreationReceipt{Subscription: ports.SubscriptionRecord{ID: sid, UserID: r.row.OwnerID, IndexerIDs: []uuid.UUID{uuid.New()}}}
	reads := 0
	var readErr error
	s := NewMediaRequestService(r, func(f ports.MediaRequestApprovalFence) ports.SubscriptionCreator {
		if f.RequestID != r.row.ID || f.Key != "receipt-key" || f.ExpectedVersion != 0 {
			t.Fatal("read-only restoration changed the command identity")
		}
		return requestReceiptReaderTest{requestCreatorTest: func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
			t.Fatal("receipt read entered creation")
			return nil, ports.ErrUnavailable
		}, read: func(ctx context.Context, a *ports.Actor, key string) (*ports.SubscriptionCreationReceipt, error) {
			reads++
			if a.ID != admin.ID || key != "media.request.approve:"+r.row.ID.String()+":receipt-key" {
				t.Fatal("receipt reader redirected actor or key")
			}
			return original, readErr
		}}
	})
	got, err := s.ApprovalReceipt(t.Context(), admin, r.row.ID, "receipt-key")
	if err != nil || got == nil || got.RequestID != r.row.ID || got.Subscription.Subscription.ID != sid || reads != 1 {
		t.Fatal("original approval receipt unavailable", got, err, reads)
	}
	original.Subscription.IndexerIDs[0] = uuid.New()
	if got.Subscription.Subscription.IndexerIDs[0] == original.Subscription.IndexerIDs[0] {
		t.Fatal("receipt result shares mutable input")
	}
	for _, mode := range []string{"foreign-owner", "foreign-subscription", "missing-link", "storage-failure", "member"} {
		t.Run(mode, func(t *testing.T) {
			oldUser, oldID, oldLink := original.Subscription.UserID, original.Subscription.ID, r.row.SubscriptionID
			actor := *admin
			want := ports.ErrUnavailable
			before := reads
			switch mode {
			case "foreign-owner":
				original.Subscription.UserID = uuid.New()
			case "foreign-subscription":
				original.Subscription.ID = uuid.New()
			case "missing-link":
				r.row.SubscriptionID = nil
			case "storage-failure":
				readErr = errors.New("private storage diagnostic")
			case "member":
				actor.Role = ports.RoleMember
				want = ports.ErrForbidden
			}
			out, err := s.ApprovalReceipt(t.Context(), &actor, r.row.ID, "receipt-key")
			if out != nil || !errors.Is(err, want) {
				t.Fatal("unverified approval receipt accepted", mode, out, err)
			}
			if mode == "member" && reads != before {
				t.Fatal("member reached approval receipt reader")
			}
			original.Subscription.UserID, original.Subscription.ID, r.row.SubscriptionID = oldUser, oldID, oldLink
			readErr = nil
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := reads
	if out, err := s.ApprovalReceipt(ctx, admin, r.row.ID, "receipt-key"); out != nil || !errors.Is(err, context.Canceled) || reads != before {
		t.Fatal("cancelled receipt read escaped", out, err)
	}
	if r.submits != 0 || r.closes != 0 {
		t.Fatal("receipt read wrote request state")
	}
}
