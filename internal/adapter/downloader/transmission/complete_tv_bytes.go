package transmission

import "github.com/dalamudx/PulseMedia/internal/ports"

// CompleteTVByteSender preserves exact inspected bytes, operation labels and the
// native affirmative ACK. Only a received session-409 permits a rejected retry.
func (c *Client) CompleteTVByteSender() ports.DownloadClient { return &automaticTVByteClient{c} }
