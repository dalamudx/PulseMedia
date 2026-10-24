package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func notifyAutomaticTVOrganization(receipt *ports.AutomaticTVObservationReceipt, err error, taskID uuid.UUID, wake chan<- struct{}) {
	if err != nil || receipt == nil || !receipt.Complete || receipt.ReviewRequired || taskID == uuid.Nil || receipt.TaskID != taskID {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}
