package app

import (
	"errors"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/rtorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/emby"
	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/jellyfin"
	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/plex"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// NativeDownloadClient constructs only the explicitly configured native protocol.
func NativeDownloadClient(kind, baseURL, username, credential string) (ports.DownloadClient, error) {
	switch kind {
	case "qbittorrent":
		return qbittorrent.NewClient(baseURL, username, credential, nil)
	case "transmission":
		return transmission.NewClient(baseURL, username, credential, nil)
	case "rtorrent":
		return rtorrent.NewAuthenticatedClient(baseURL, username, credential, nil)
	default:
		return nil, errors.New("native download client unavailable")
	}
}

// NativeMediaServerClient constructs the explicitly configured native protocol.
func NativeMediaServerClient(kind, baseURL, credential string) (ports.MediaServerClient, error) {
	switch kind {
	case "jellyfin":
		return jellyfin.NewClient(baseURL, credential, nil), nil
	case "emby":
		return emby.NewClient(baseURL, credential, nil), nil
	case "plex":
		return plex.NewClient(baseURL, credential, nil), nil
	default:
		return nil, errors.New("native media server unavailable")
	}
}
