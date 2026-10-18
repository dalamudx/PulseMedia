package ports

import (
	"encoding/base32"
	"encoding/hex"
	"net/url"
	"strings"
)

// DownloadTorrentSourceFactory binds native fetching to the configured endpoint.
// Implementations must reject cross-origin URLs and redirects, bound response
// bodies, and return safe port errors. Credentials are ephemeral.
type DownloadTorrentSourceFactory func(kind, endpointURL, credential string) IndexerClient

// DownloadMagnetHash admits v1, v2 and hybrid identities without HTTP metadata sources.
// Hybrid torrents use their v1 hash, matching the downloader task identity.
// Tracker discovery remains part of the existing BitTorrent contract.
func DownloadMagnetHash(raw string) (string, error) {
	if len(raw) > 16384 {
		return "", ErrBadResponse
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "magnet" || u.Host != "" || u.User != nil || u.Path != "" || u.Opaque != "" || u.Fragment != "" {
		return "", ErrBadResponse
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["xt"]) == 0 || len(q["xt"]) > 2 {
		return "", ErrBadResponse
	}
	for k := range q {
		switch k {
		case "xt", "dn", "tr", "xl", "kt":
		default:
			return "", ErrBadResponse
		}
	}
	var v1, v2 string
	for _, xt := range q["xt"] {
		switch {
		case strings.HasPrefix(xt, "urn:btih:"):
			if v1 != "" {
				return "", ErrBadResponse
			}
			h := strings.TrimPrefix(xt, "urn:btih:")
			var b []byte
			switch len(h) {
			case 40:
				b, err = hex.DecodeString(h)
			case 32:
				b, err = base32.StdEncoding.DecodeString(strings.ToUpper(h))
			default:
				return "", ErrBadResponse
			}
			if err != nil || len(b) != 20 {
				return "", ErrBadResponse
			}
			v1 = hex.EncodeToString(b)
		case strings.HasPrefix(xt, "urn:btmh:1220"):
			if v2 != "" {
				return "", ErrBadResponse
			}
			b, e := hex.DecodeString(strings.TrimPrefix(xt, "urn:btmh:1220"))
			if e != nil || len(b) != 32 {
				return "", ErrBadResponse
			}
			v2 = hex.EncodeToString(b)
		default:
			return "", ErrBadResponse
		}
	}
	if v1 != "" {
		return v1, nil
	}
	return v2, nil
}

// ValidateDownloadPayload prevents adapters from becoming arbitrary URL fetchers.
func ValidateDownloadPayload(p AddTorrentParam) error {
	if len(p.TorrentBytes) > 0 {
		if p.DownloadURL != "" || len(p.TorrentBytes) > MaxTorrentMetainfoBytes {
			return ErrBadResponse
		}
		return nil
	}
	hash, err := DownloadMagnetHash(p.DownloadURL)
	if err != nil || (p.InfoHash != "" && !strings.EqualFold(hash, p.InfoHash)) {
		return ErrBadResponse
	}
	return nil
}
