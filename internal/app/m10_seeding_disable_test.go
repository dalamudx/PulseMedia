package app

import (
	"net/http"
	"testing"
)

type movieSeedingReadGate struct {
	entered chan struct{}
	release chan struct{}
}

func (p *movieSeedingProtocol) awaitFacts(r *http.Request) bool {
	gate := p.factGate.Load()
	if gate == nil || r.URL.Query().Get("hashes") == "" {
		return true
	}
	select {
	case gate.entered <- struct{}{}:
	default:
	}
	select {
	case <-gate.release:
		return true
	case <-r.Context().Done():
		return false
	}
}

func TestM10SeedingDisableDuringNativeReadRuntimePG(t *testing.T) {
	runM10SeedingNativeState(t, false, true)
}
