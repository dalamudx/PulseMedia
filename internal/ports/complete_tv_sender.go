package ports

import (
	"context"
	"errors"
)

// ErrCompleteTVNeedsObservation never grants permission to repeat an Add.
var ErrCompleteTVNeedsObservation = errors.New("complete TV submission needs observation; protection retained")

// CompleteTVTorrentSource is an ephemeral decrypted input, never persisted or logged.
type CompleteTVTorrentSource struct {
	EndpointURL string `json:"-"`
	APIKey      string `json:"-"`
	DownloadURL string `json:"-"`
}

// CompleteTVTorrentFetcher fetches the original bytes once without redirects.
type CompleteTVTorrentFetcher interface {
	FetchCompleteTVTorrent(context.Context, CompleteTVTorrentSource) ([]byte, error)
}

// TorrentBytes returns an owned copy of the exact inspected metainfo.
func (p *CompleteTVMetainfoProof) TorrentBytes() []byte { return p.Bytes() }
