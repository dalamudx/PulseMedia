package app

import "testing"

func TestM10SeedingTransmissionRemovalRuntimePG(t *testing.T) {
	runM10SeedingNativeProtocol(t, false, false, "transmission")
}
func TestM10SeedingTransmissionLostReceiptRuntimePG(t *testing.T) {
	runM10SeedingNativeProtocol(t, true, false, "transmission")
}
