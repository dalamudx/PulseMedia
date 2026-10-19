package mediaserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type endpointProbeReader struct {
	ports.MediaConfirmationRepository
	id        uuid.UUID
	err       error
	afterRead func()
}

func (r endpointProbeReader) LoadMediaServerConnection(context.Context, uuid.UUID) (ports.MediaServerConnection, error) {
	if r.afterRead != nil {
		r.afterRead()
	}
	return ports.MediaServerConnection{ID: r.id}, r.err
}

type endpointProbeClient struct {
	ports.MediaServerClient
	err   error
	calls int
}

func (c *endpointProbeClient) Test(context.Context) error { c.calls++; return c.err }

func TestEndpointProbeDistinguishesReadAndNativeFailure(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		readErr, nativeErr, want      error
		unconfigured, cancelAfterRead bool
		calls                         int
		alive                         bool
	}{
		{name: "missing", readErr: ports.ErrNotFound, want: ports.ErrNotFound},
		{name: "storage", readErr: errors.New("postgres://hidden-secret@example.invalid/database"), want: ports.ErrUnavailable},
		{name: "unconfigured", unconfigured: true, want: ports.ErrUnavailable},
		{name: "native", nativeErr: errors.New("http://example.invalid?token=hidden-secret"), want: ports.ErrEndpointProbeFailed, calls: 1},
		{name: "cancelled after read", cancelAfterRead: true, want: context.Canceled},
		{name: "reachable", calls: 1, alive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id := uuid.New()
			reader := endpointProbeReader{id: id, err: tc.readErr}
			if tc.cancelAfterRead {
				reader.afterRead = cancel
			}
			service := NewSyncService(nil, reader)
			client := &endpointProbeClient{err: tc.nativeErr}
			if !tc.unconfigured {
				service.RegisterClient(id, client)
			}
			alive, message, err := service.TestServer(ctx, id)
			if alive != tc.alive || !errors.Is(err, tc.want) || client.calls != tc.calls {
				t.Fatalf("alive=%v err=%v calls=%d", alive, err, client.calls)
			}
			if strings.Contains(message, "hidden-secret") || err != nil && strings.Contains(err.Error(), "hidden-secret") {
				t.Fatal("probe exposed endpoint or storage credentials")
			}
			if tc.nativeErr != nil && !errors.Is(err, ErrServerUnreachable) {
				t.Fatal("legacy native failure classification lost")
			}
		})
	}
}
