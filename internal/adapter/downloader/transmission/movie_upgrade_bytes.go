package transmission

import "github.com/dalamudx/PulseMedia/internal/ports"

// MovieUpgradeByteSender requires the dedicated caller's private dispatch permit.
func (c *Client) MovieUpgradeByteSender() ports.DownloadClient { return &automaticTVByteClient{c} }
