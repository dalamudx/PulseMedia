package httpapi_test

import "testing"

func TestPublicUpgradePreparationAppCookieRestart(t *testing.T) {
	var start func() func()
	var stop func()
	f := newPublicPreparationFixture(t, func(f *publicPreparationFixture) {
		start = publicUpgradeAppStarter(t, f.publicUpgradeFixture)
		stop = start()
	})
	t.Cleanup(func() { stop() })
	first := f.prepared(t, "app-preparation-original")
	f.noSend(t, 1)
	stop()
	stop = start()
	recovered := f.prepared(t, "app-preparation-original")
	if recovered.Body.String() != first.Body.String() || f.fetches.Load() != 1 {
		t.Fatal("restart changed receipt or fetched twice")
	}
	f.noSend(t, 1)
}
