package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

type pushExecution struct {
	owned       ports.WebPushOwnedDelivery
	loads       int
	failAt      int
	outcome     string
	closed      bool
	completeErr error
}

func (e *pushExecution) Load(context.Context, uuid.UUID, string, int64, string) (*ports.WebPushOwnedDelivery, error) {
	e.loads++
	if e.loads == e.failAt {
		return nil, ports.ErrForbidden
	}
	return &e.owned, nil
}
func (e *pushExecution) Complete(_ context.Context, _ uuid.UUID, _ string, _ int64, outcome string) error {
	e.outcome = outcome
	return e.completeErr
}
func (e *pushExecution) Close() error { e.closed = true; return nil }

type pushExecutionRepo struct{ e *pushExecution }

func (r pushExecutionRepo) OpenWebPushExecution(context.Context, uuid.UUID) (ports.WebPushDeliveryExecution, error) {
	return r.e, nil
}

type pushDecoder struct {
	plain []byte
	err   error
}

func (d pushDecoder) DecryptSecret([]byte) ([]byte, error) { return d.plain, d.err }

type pushSender func(context.Context, ports.WebPushSubscription, ports.WebPushPayload) error

func (f pushSender) SendNotification(ctx context.Context, s ports.WebPushSubscription, p ports.WebPushPayload) error {
	return f(ctx, s, p)
}

type pushFailure struct {
	calls   int
	delay   time.Duration
	message string
}

func (f *pushFailure) Fail(_ context.Context, _ uuid.UUID, _ string, _ int64, message string, delay time.Duration) error {
	f.calls++
	f.delay = delay
	f.message = message
	return nil
}
func TestWebPushDeliveryAuthorityAndStableTag(t *testing.T) {
	_, sub, _ := pushInput(t)
	raw, _ := json.Marshal(sub)
	for _, failAt := range []int{0, 1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			e := &pushExecution{owned: ports.WebPushOwnedDelivery{EventID: uuid.New(), Ciphertext: []byte("encrypted")}, failAt: failAt}
			calls := 0
			service := notify.NewWebPushDeliveryService(pushExecutionRepo{e}, pushDecoder{plain: raw}, &pushFailure{}, pushSender(func(_ context.Context, s ports.WebPushSubscription, p ports.WebPushPayload) error {
				calls++
				if s != sub || p.Tag != "pulsemedia:"+e.owned.EventID.String() || p.URL != "/" {
					t.Fatal("wrong recipient or payload")
				}
				return nil
			}), "version")
			if err := service.Dispatch(t.Context(), uuid.New(), uuid.New(), "owner", 1); err != nil {
				t.Fatal(err)
			}
			if failAt == 0 {
				if calls != 1 || e.loads != 2 || e.outcome != "accepted" {
					t.Fatal("successful send not fenced")
				}
			} else if calls != 0 || e.outcome != "skipped" {
				t.Fatal("unauthorized send")
			}
			if !e.closed {
				t.Fatal("physical execution leaked")
			}
		})
	}
}
func TestWebPushDeliveryExpiryRateAndPrivateErrors(t *testing.T) {
	_, sub, _ := pushInput(t)
	raw, _ := json.Marshal(sub)
	for _, tc := range []struct {
		name    string
		err     error
		outcome string
		delay   time.Duration
	}{{"expired", ports.ErrNotFound, "expired", 0}, {"rate", ports.NewRateLimitError(3*time.Minute, "private endpoint"), "", 3 * time.Minute}, {"failure", errors.New("private browser capability"), "", 10 * time.Second}} {
		t.Run(tc.name, func(t *testing.T) {
			e := &pushExecution{owned: ports.WebPushOwnedDelivery{EventID: uuid.New()}}
			failure := &pushFailure{}
			service := notify.NewWebPushDeliveryService(pushExecutionRepo{e}, pushDecoder{plain: raw}, failure, pushSender(func(context.Context, ports.WebPushSubscription, ports.WebPushPayload) error { return tc.err }), "version")
			if err := service.Dispatch(t.Context(), uuid.New(), uuid.New(), "owner", 1); err != nil {
				t.Fatal(err)
			}
			if e.outcome != tc.outcome || failure.delay != tc.delay || !e.closed {
				t.Fatal("wrong error handling")
			}
			if failure.message != "" && failure.message != "browser push service unavailable" {
				t.Fatal("private error persisted")
			}
		})
	}
}
func TestWebPushDeliveryCancellationRetainsPhysicalExecution(t *testing.T) {
	_, sub, _ := pushInput(t)
	raw, _ := json.Marshal(sub)
	e := &pushExecution{owned: ports.WebPushOwnedDelivery{EventID: uuid.New()}}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	service := notify.NewWebPushDeliveryService(pushExecutionRepo{e}, pushDecoder{plain: raw}, &pushFailure{}, pushSender(func(context.Context, ports.WebPushSubscription, ports.WebPushPayload) error {
		close(entered)
		<-release
		return context.Canceled
	}), "version")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { done <- service.Dispatch(ctx, uuid.New(), uuid.New(), "owner", 1) }()
	<-entered
	cancel()
	select {
	case <-done:
		t.Fatal("returned before physical I/O")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) || !e.closed {
		t.Fatal("cancel contract", err)
	}
}
