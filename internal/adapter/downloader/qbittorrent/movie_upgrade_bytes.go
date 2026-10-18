package qbittorrent

import "github.com/dalamudx/PulseMedia/internal/ports"

// MovieUpgradeByteSender uses the same affirmative, no-replay exact-byte transport.
// Dedicated upgrade execution still requires a privately issued dispatch permit.
func (c *Client) MovieUpgradeByteSender() ports.DownloadClient { return &completeTVByteClient{c} }
