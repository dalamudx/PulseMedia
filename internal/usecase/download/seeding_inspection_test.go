package download

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type inspectionClock struct{ now time.Time }

func (c inspectionClock) Now() time.Time                  { return c.now }
func (c inspectionClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

type inspectionRepository struct {
	prepared *ports.PreparedSeedingInspection
}

func (r inspectionRepository) PrepareSeedingInspection(context.Context, *ports.Actor, uuid.UUID) (*ports.PreparedSeedingInspection, error) {
	return r.prepared, nil
}

type inspectionReader struct {
	read func(context.Context, string, string) (*ports.SeedingRemoteTorrent, error)
}

func (r inspectionReader) ReadSeedingTorrent(ctx context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
	return r.read(ctx, hash, tag)
}
func (inspectionReader) ReadSeedingFreeSpace(context.Context, string) (ports.SeedingPathSpace, error) {
	panic("manual inspection requested free space")
}

func TestSeedingInspectionFactsAndIdentity(t *testing.T) {
	op := uuid.New()
	hash := strings.Repeat("a", 40)
	tag := "pulsemedia:op:" + op.String()
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		change func(*ports.SeedingRemoteTorrent)
		bad    bool
	}{
		{"unknown", func(*ports.SeedingRemoteTorrent) {}, false},
		{"known zero", func(r *ports.SeedingRemoteTorrent) {
			r.Facts.ShareRatio = new(float64)
			r.Facts.SeedingSeconds = new(int64)
		}, false},
		{"infinite", func(r *ports.SeedingRemoteTorrent) { r.Facts.InfiniteShareRatio = true }, false},
		{"large seconds", func(r *ports.SeedingRemoteTorrent) { r.Facts.SeedingSeconds = new(int64(math.MaxInt64)) }, false},
		{"wrong hash", func(r *ports.SeedingRemoteTorrent) { r.InfoHash = strings.Repeat("b", 40) }, true},
		{"wrong tag", func(r *ports.SeedingRemoteTorrent) { r.OperationTag = "other" }, true},
		{"wrong path", func(r *ports.SeedingRemoteTorrent) { r.SavePath = "/elsewhere" }, true},
		{"negative bytes", func(r *ports.SeedingRemoteTorrent) { r.TotalBytes = -1 }, true},
		{"nan ratio", func(r *ports.SeedingRemoteTorrent) { r.Facts.ShareRatio = new(math.NaN()) }, true},
		{"infinite number", func(r *ports.SeedingRemoteTorrent) { r.Facts.ShareRatio = new(math.Inf(1)) }, true},
		{"negative ratio", func(r *ports.SeedingRemoteTorrent) { r.Facts.ShareRatio = new(float64(-1)) }, true},
		{"conflicting ratio", func(r *ports.SeedingRemoteTorrent) {
			r.Facts.ShareRatio = new(float64(0))
			r.Facts.InfiniteShareRatio = true
		}, true},
		{"negative seconds", func(r *ports.SeedingRemoteTorrent) { r.Facts.SeedingSeconds = new(int64(-1)) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := &ports.SeedingRemoteTorrent{InfoHash: hash, OperationTag: tag, SavePath: "/downloads", TotalBytes: 100, Facts: seeding.Observation{Complete: true}}
			tc.change(remote)
			released, validations := false, 0
			p := &ports.PreparedSeedingInspection{InfoHash: hash, OperationTag: tag, SavePath: "/downloads", Release: func() { released = true }}
			p.Validate = func(context.Context) error {
				if released {
					t.Error("budget released before validation")
				}
				validations++
				return nil
			}
			p.Reader = inspectionReader{read: func(ctx context.Context, h, g string) (*ports.SeedingRemoteTorrent, error) {
				if h != hash || g != tag || released {
					t.Error("wrong read identity")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 15*time.Second {
					t.Error("missing bounded native deadline")
				}
				return remote, nil
			}}
			got, err := NewSeedingInspectionService(inspectionRepository{p}, inspectionClock{now}).Inspect(t.Context(), nil, op)
			if !released || validations != 2 {
				t.Fatal("budget/validation lifecycle", released, validations)
			}
			if tc.bad {
				if got != nil || !errors.Is(err, ports.ErrBadResponse) {
					t.Fatal(got, err)
				}
				return
			}
			if err != nil || got == nil || !got.ObservedAt.Equal(now) || !got.Complete || got.InfiniteShareRatio != remote.Facts.InfiniteShareRatio {
				t.Fatal(got, err)
			}
			if (got.ShareRatio == nil) != (remote.Facts.ShareRatio == nil) || (got.SeedingSeconds == nil) != (remote.Facts.SeedingSeconds == nil) {
				t.Fatal("unknown facts became zero")
			}
			if tc.name == "large seconds" && *got.SeedingSeconds != "9223372036854775807" {
				t.Fatal("lost integer precision")
			}
			if got.ShareRatio != nil {
				old := *got.ShareRatio
				*remote.Facts.ShareRatio = 99
				if *got.ShareRatio != old {
					t.Fatal("returned mutable native fact")
				}
			}
		})
	}
}

func TestSeedingInspectionDiscardsLateAndRevokedResults(t *testing.T) {
	for _, mode := range []string{"cancel", "revoke", "read error", "invalid original"} {
		t.Run(mode, func(t *testing.T) {
			op := uuid.New()
			hash := strings.Repeat("a", 40)
			tag := "pulsemedia:op:" + op.String()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls, validations := 0, 0
			released := false
			p := &ports.PreparedSeedingInspection{InfoHash: hash, OperationTag: tag, SavePath: "/downloads", Release: func() { released = true }}
			p.Validate = func(context.Context) error {
				validations++
				if mode == "revoke" && validations == 2 {
					return ports.ErrForbidden
				}
				return nil
			}
			p.Reader = inspectionReader{read: func(context.Context, string, string) (*ports.SeedingRemoteTorrent, error) {
				calls++
				if mode == "cancel" {
					cancel()
				}
				if mode == "read error" {
					return &ports.SeedingRemoteTorrent{}, ports.ErrTimeout
				}
				return &ports.SeedingRemoteTorrent{InfoHash: hash, OperationTag: tag, SavePath: "/downloads"}, nil
			}}
			if mode == "invalid original" {
				p.InfoHash = "not-a-hash"
			}
			got, err := NewSeedingInspectionService(inspectionRepository{p}, inspectionClock{time.Now()}).Inspect(ctx, nil, op)
			if got != nil || err == nil || !released {
				t.Fatal("late facts published", got, err)
			}
			if mode != "invalid original" && validations != 2 {
				t.Fatal("missing final validation after failed or cancelled native read", validations)
			}
			if mode == "invalid original" && calls != 0 {
				t.Fatal("invalid original touched network")
			}
		})
	}
}
