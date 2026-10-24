package downloadmetrics

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const nativeReadTimeout = 5 * time.Second

type Service struct {
	repo    ports.DownloadGlobalRatesRepository
	builder ports.DownloadGlobalRatesBuilder
	clock   ports.Clock
}

func NewService(repo ports.DownloadGlobalRatesRepository, builder ports.DownloadGlobalRatesBuilder, clock ports.Clock) *Service {
	return &Service{repo: repo, builder: builder, clock: clock}
}

func (s *Service) Execute(ctx context.Context, lease ports.JobLease) error {
	if s == nil || s.repo == nil || s.builder == nil || s.clock == nil {
		return ports.ErrUnavailable
	}
	if lease.ID == uuid.Nil || lease.Token < 1 || strings.TrimSpace(lease.Owner) == "" || strings.TrimSpace(lease.Owner) != lease.Owner {
		return ports.ErrRejected
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	permit, err := s.repo.Acquire(ctx, lease)
	if err != nil {
		return safeStoreError(ctx, err)
	}
	if permit == nil {
		return ports.ErrUnavailable
	}
	defer permit.Close()
	source := permit.Snapshot()
	digest, e := hex.DecodeString(source.ConfigStamp)
	if source.ClientID == uuid.Nil || e != nil || len(digest) != 32 || source.ConfigStamp != strings.ToLower(source.ConfigStamp) || strings.TrimSpace(source.URL) == "" || (source.ClientType != "qbittorrent" && source.ClientType != "transmission") {
		return ports.ErrRejected
	}
	if err = permit.Check(ctx); err != nil {
		return safeStoreError(ctx, err)
	}
	reader, err := s.builder.NewDownloadGlobalRatesReader(source)
	var rates ports.DownloadGlobalRates
	if err == nil && reader == nil {
		err = ports.ErrUnavailable
	}
	if err == nil {
		callCtx, cancel := context.WithTimeout(ctx, nativeReadTimeout)
		rates, err = reader.ReadGlobalRates(callCtx)
		if callCtx.Err() != nil {
			err = callCtx.Err()
		}
		cancel()
	}
	// Withdraw publication on cancellation, after the actual native call returns.
	if e := ctx.Err(); e != nil {
		return e
	}
	observation := ports.DownloadGlobalRatesObservation{ObservedAt: s.clock.Now().UTC()}
	if observation.ObservedAt.IsZero() {
		return ports.ErrUnavailable
	}
	if err == nil && ((rates.DownloadBytesPerSecond != nil && *rates.DownloadBytesPerSecond < 0) || (rates.UploadBytesPerSecond != nil && *rates.UploadBytesPerSecond < 0)) {
		err = ports.ErrBadResponse
	}
	if err != nil {
		observation.Failure, observation.RetryAfter = classifyFailure(err)
	} else {
		// Detach optional values from an adapter's reusable response buffers.
		if rates.DownloadBytesPerSecond != nil {
			value := *rates.DownloadBytesPerSecond
			rates.DownloadBytesPerSecond = &value
		}
		if rates.UploadBytesPerSecond != nil {
			value := *rates.UploadBytesPerSecond
			rates.UploadBytesPerSecond = &value
		}
		observation.Rates = &rates
	}
	return safeStoreError(ctx, permit.Finish(ctx, observation))
}

func classifyFailure(err error) (ports.DownloadGlobalRatesFailure, time.Duration) {
	var limited *ports.RateLimitError
	if errors.As(err, &limited) {
		delay := limited.RetryAfter
		if delay < 0 {
			delay = 0
		}
		if delay > 24*time.Hour {
			delay = 24 * time.Hour
		}
		return ports.DownloadGlobalRatesLimited, delay
	}
	switch {
	case errors.Is(err, ports.ErrAuthRequired):
		return ports.DownloadGlobalRatesAuth, 0
	case errors.Is(err, ports.ErrTimeout), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return ports.DownloadGlobalRatesTimeout, 0
	case errors.Is(err, ports.ErrBlocked):
		return ports.DownloadGlobalRatesBlocked, 0
	case errors.Is(err, ports.ErrBadResponse), errors.Is(err, ports.ErrRejected):
		return ports.DownloadGlobalRatesBadResponse, 0
	default:
		return ports.DownloadGlobalRatesUnavailable, 0
	}
}

func safeStoreError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	for _, safe := range []error{ports.ErrUnavailable, ports.ErrRejected, ports.ErrStaleDeliveryLease, ports.ErrEndpointVersionConflict, ports.ErrDownloadTelemetryStale, ports.ErrTimeout} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ports.ErrUnavailable
}
