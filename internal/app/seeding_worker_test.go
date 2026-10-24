package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type seedingSourceFunc func(context.Context, uuid.UUID, string) (ports.SeedingClientSource, error)

func (f seedingSourceFunc) ReadSeedingClientSource(c context.Context, id uuid.UUID, stamp string) (ports.SeedingClientSource, error) {
	return f(c, id, stamp)
}
func TestSeedingFactoryConstructsWithoutNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, kind := range []string{"qbittorrent", "transmission"} {
		t.Run(kind, func(t *testing.T) {
			id := uuid.New()
			stamp := strings.Repeat("a", 64)
			factory := &seedingClientFactory{sources: seedingSourceFunc(func(_ context.Context, actual uuid.UUID, version string) (ports.SeedingClientSource, error) {
				if actual != id || version != stamp {
					t.Fatal("source read changed binding")
				}
				return ports.SeedingClientSource{ClientID: id, ConfigStamp: stamp, Kind: kind, URL: server.URL, Username: "owned", Credential: "owned-credential"}, nil
			})}
			native, err := factory.OpenSeedingExecution(t.Context(), id, stamp)
			if err != nil || native.Reader == nil || native.Remover == nil || calls.Load() != 0 {
				t.Fatal("native construction performed network I/O", err)
			}
		})
	}
}
func TestSeedingFactoryRejectsChangedSource(t *testing.T) {
	id := uuid.New()
	stamp := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name      string
		source    ports.SeedingClientSource
		err, want error
	}{
		{name: "store", err: ports.ErrUnavailable, want: ports.ErrUnavailable},
		{name: "identity", source: ports.SeedingClientSource{ClientID: uuid.New(), ConfigStamp: stamp}, want: ports.ErrSeedingConfigConflict},
		{name: "version", source: ports.SeedingClientSource{ClientID: id, ConfigStamp: strings.Repeat("b", 64)}, want: ports.ErrSeedingConfigConflict},
		{name: "unsupported", source: ports.SeedingClientSource{ClientID: id, ConfigStamp: stamp, Kind: "unknown"}, want: ports.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &seedingClientFactory{sources: seedingSourceFunc(func(context.Context, uuid.UUID, string) (ports.SeedingClientSource, error) { return tc.source, tc.err })}
			v, err := f.OpenSeedingExecution(t.Context(), id, stamp)
			if !errors.Is(err, tc.want) || v.Reader != nil || v.Remover != nil {
				t.Fatal("unsafe factory result", err)
			}
		})
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	f := &seedingClientFactory{sources: seedingSourceFunc(func(context.Context, uuid.UUID, string) (ports.SeedingClientSource, error) {
		t.Fatal("cancelled factory read source")
		return ports.SeedingClientSource{}, nil
	})}
	if _, err := f.OpenSeedingExecution(cancelled, id, stamp); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := (*seedingClientFactory)(nil).OpenSeedingExecution(t.Context(), id, stamp); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
}
