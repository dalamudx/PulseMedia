//go:build linux

package app

import "testing"

func TestM8UntrackedOrganizationSparseTVRuntimeCookiePG(t *testing.T) {
	m8UntrackedOrganizationRequired(t)
	f := m8UntrackedOrganizationNew(t, "hardlink", "tv")
	history := f.confirmationHistory(t)
	receipt, raw := f.queue(t)
	f.start(t)
	f.completed(t, receipt)
	f.stop(t)
	f.verifyImport(t, receipt, "hardlink")
	f.recoverOffline(t, receipt, raw, "completed")
	if history != f.confirmationHistory(t) {
		t.Fatal("sparse TV organization changed original confirmation history")
	}
	f.log(t, "sparse_tv_one_file_S01E02_S01E07", 2)
}
