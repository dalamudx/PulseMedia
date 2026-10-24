package app

import "testing"

func TestM10SeedingNativeFinishRollbackRuntimePG(t *testing.T) {
	runM10SeedingNativeScenario(t, m10SeedingNativeScenario{kind: "qbittorrent", finishRollback: true})
}
func TestM10SeedingTransmissionFinishRollbackRuntimePG(t *testing.T) {
	runM10SeedingNativeScenario(t, m10SeedingNativeScenario{kind: "transmission", finishRollback: true})
}
