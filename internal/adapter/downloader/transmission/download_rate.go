package transmission

import "encoding/json"

// Optional display telemetry cannot invalidate otherwise valid task evidence.
func nativeDownloadRate(raw json.RawMessage) *int64 {
	var rate *int64
	if json.Unmarshal(raw, &rate) != nil || rate == nil || *rate < 0 {
		return nil
	}
	return rate
}
