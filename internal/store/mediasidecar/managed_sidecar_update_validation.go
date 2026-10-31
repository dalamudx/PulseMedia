package mediasidecar

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"reflect"
)

func managedFrozen(raw []byte, t ports.ManagedSidecarUpdateTicket) error {
	var original ports.ManagedSidecarUpdateTicket
	if json.Unmarshal(raw, &original) != nil {
		return ports.ErrUnavailable
	}
	original.Permit = t.Permit
	if !reflect.DeepEqual(original, t) {
		return ports.ErrMediaSidecarStale
	}
	return nil
}
