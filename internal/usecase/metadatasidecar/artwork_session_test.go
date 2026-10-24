package metadatasidecar_test

import (
	"context"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatasidecar"
)

type artSession struct {
	artsProvider
	validate func(context.Context) error
}

func (s artSession) Validate(ctx context.Context) error { return s.validate(ctx) }

type artSource func(context.Context) (ports.MediaArtworkSession, error)

func (s artSource) Open(ctx context.Context) (ports.MediaArtworkSession, error) { return s(ctx) }

func TestArtworkConfigurationFencedBeforeEachPublish(t *testing.T) {
	for _, allowed := range []int{0, 1, 4} {
		t.Run(string(rune('0'+allowed)), func(t *testing.T) {
			f := setup(t, movieSnapshot())
			checks := 0
			session := artSession{artsProvider: artsProvider{get: func(_ context.Context, id ports.ArtworkIdentity) (ports.MediaArtworks, error) { return allArt(id), nil }, fetch: func(context.Context, string) ([]byte, error) { return []byte("art"), nil }}, validate: func(context.Context) error {
				checks++
				if checks > allowed {
					return ports.ErrUnavailable
				}
				return nil
			}}
			source := artSource(func(context.Context) (ports.MediaArtworkSession, error) { return session, nil })
			svc := metadatasidecar.NewService(f.repo, f.fs, nil, nil, metadatasidecar.WithArtworkSource(source, nil))
			if err := svc.Execute(f.ctx, f.lease); err != nil {
				t.Fatal(err)
			}
			if len(f.fs.requests) != allowed+1 || f.repo.completion == nil || len(f.repo.completion.Artifacts) != allowed+1 {
				t.Fatal("changed configuration published or erased prior outcomes")
			}
		})
	}
	f := setup(t, movieSnapshot())
	source := artSource(func(context.Context) (ports.MediaArtworkSession, error) { return nil, ports.ErrUnavailable })
	if err := metadatasidecar.NewService(f.repo, f.fs, nil, nil, metadatasidecar.WithArtworkSource(source, nil)).Execute(f.ctx, f.lease); err != nil || len(f.fs.requests) != 1 {
		t.Fatal("missing optional configuration suppressed NFO", err)
	}
}
