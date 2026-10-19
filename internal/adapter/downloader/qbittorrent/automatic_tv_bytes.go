package qbittorrent

import "github.com/dalamudx/PulseMedia/internal/ports"

// AutomaticTVByteSender exposes byte submission and protocol acknowledgement only;
// the caller must obtain automatic TV business authority before sending.
func (c *Client) AutomaticTVByteSender() ports.DownloadClient { return c.CompleteTVByteSender() }
