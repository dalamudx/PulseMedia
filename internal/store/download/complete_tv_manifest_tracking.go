package download

import "github.com/dalamudx/PulseMedia/internal/ports"

// Native export is required unless the frozen actual protocol is Transmission.
// Transmission completion is bound independently to its persisted preparation by
// the TV repository or the movie driver's privately consumed query descriptor.
func dedicatedTrackingClient(client ports.DownloadClient, kind string) (ports.CompleteTVManifestTrackingClient, ports.CompleteTVTrackingClient, error) {
	if kind == "transmission" {
		c, ok := client.(interface {
			CompleteTVManifestTrackingObserver() ports.CompleteTVManifestTrackingClient
		})
		if !ok {
			return nil, nil, ports.ErrUnavailable
		}
		native := c.CompleteTVManifestTrackingObserver()
		if native == nil {
			return nil, nil, ports.ErrUnavailable
		}
		return native, nil, nil
	}
	c, ok := client.(interface {
		CompleteTVTrackingObserver() ports.CompleteTVTrackingClient
	})
	if !ok {
		return nil, nil, ports.ErrUnavailable
	}
	native := c.CompleteTVTrackingObserver()
	if native == nil {
		return nil, nil, ports.ErrUnavailable
	}
	return native, native, nil
}
