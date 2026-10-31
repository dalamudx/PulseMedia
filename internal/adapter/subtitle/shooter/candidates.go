package shooter

import (
	"bytes"
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/url"
	"strings"
)

var _ ports.SubtitleCandidateProvider = (*Client)(nil)

// Cache at most eight Chinese SRTs; signed links remain inside this adapter.
func (c *Client) FindSubtitleCandidates(ctx context.Context, hash ports.SubtitleVideoHash) ([]ports.SubtitleCandidateContent, error) {
	if !media.ValidShooterHash(hash.Hash, hash.SizeBytes) || c.apiURL == nil {
		return nil, ports.ErrRejected
	}
	form := url.Values{"filehash": {hash.Hash}, "pathinfo": {""}, "format": {"json"}, "lang": {"chn"}}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL.String(), strings.NewReader(form.Encode()))
	if e != nil {
		return nil, ports.ErrRejected
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, e := c.readResponse(ctx, req, maxAPIBytes)
	if e != nil {
		return nil, e
	}
	if bytes.Equal(body, []byte{0xff}) {
		return []ports.SubtitleCandidateContent{}, nil
	}
	links, e := c.subtitleLinks(body)
	if e != nil {
		return nil, e
	}
	if len(links) > 8 {
		links = links[:8]
	}
	out := make([]ports.SubtitleCandidateContent, 0, len(links))
	for _, link := range links {
		req, e = http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if e != nil {
			return nil, ports.ErrBadResponse
		}
		body, e = c.readResponse(ctx, req, ports.MaxSubtitleBytes)
		if e != nil {
			return nil, e
		}
		normalized, e := media.NormalizeChineseSRT(body)
		if e != nil {
			return nil, ports.ErrBadResponse
		}
		out = append(out, ports.SubtitleCandidateContent{Content: normalized})
	}
	return out, nil
}
