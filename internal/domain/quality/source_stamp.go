package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// AcquisitionSourceStamp freezes material source identity, not volatile swarm
// counters or a mutable global quality rank. Ciphertext stays opaque to rules.
func AcquisitionSourceStamp(title, infoHash string, cipher, profile []byte) string {
	raw, _ := json.Marshal([]any{title, strings.ToLower(infoHash), cipher, profile})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
