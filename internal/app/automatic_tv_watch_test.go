package app

import (
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestAutomaticTVWatchOrganizationWake(t *testing.T) {
	task := uuid.New()
	for _, test := range []struct {
		name    string
		receipt *ports.AutomaticTVObservationReceipt
		err     error
		want    bool
	}{
		{"nil", nil, nil, false},
		{"tracking", &ports.AutomaticTVObservationReceipt{TaskID: task}, nil, false},
		{"review", &ports.AutomaticTVObservationReceipt{TaskID: task, Complete: true, ReviewRequired: true}, nil, false},
		{"failed", &ports.AutomaticTVObservationReceipt{TaskID: task, Complete: true}, errors.New("commit failed"), false},
		{"different-task", &ports.AutomaticTVObservationReceipt{TaskID: uuid.New(), Complete: true}, nil, false},
		{"committed", &ports.AutomaticTVObservationReceipt{TaskID: task, Complete: true}, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			wake := make(chan struct{}, 1)
			notifyAutomaticTVOrganization(test.receipt, test.err, task, wake)
			if (len(wake) == 1) != test.want {
				t.Fatal("uncommitted/review result woke organizer", len(wake))
			}
		})
	}
	// Saturation and an absent consumer must never hold the observation worker.
	wake := make(chan struct{}, 1)
	wake <- struct{}{}
	notifyAutomaticTVOrganization(&ports.AutomaticTVObservationReceipt{TaskID: task, Complete: true}, nil, task, wake)
	notifyAutomaticTVOrganization(&ports.AutomaticTVObservationReceipt{TaskID: task, Complete: true}, nil, task, nil)
}
