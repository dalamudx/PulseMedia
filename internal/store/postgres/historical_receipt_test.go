package postgres_test

import (
	"bytes"
	"encoding/json"
)

// Historical receipts preserve their complete serialized fields and timestamps.
// Go time.Location pointers can differ after JSON decoding on a UTC host.
func sameHistoricalJSON(a, b any) bool {
	first, firstErr := json.Marshal(a)
	second, secondErr := json.Marshal(b)
	return firstErr == nil && secondErr == nil && bytes.Equal(first, second)
}
