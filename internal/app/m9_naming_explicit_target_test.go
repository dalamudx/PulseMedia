//go:build linux

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func TestM9NamingPreservesExplicitExternalOrganizationRuntimePG(t *testing.T) {
	m8UntrackedOrganizationRequired(t)
	for _, action := range []string{"hardlink", "copy", "move"} {
		t.Run(action, func(t *testing.T) {
			f := m8UntrackedOrganizationNew(t, action)
			history := f.confirmationHistory(t)
			rules, _ := json.Marshal(naming.TemplateRules{MovieTemplate: "Different/{title}.{extension}", EpisodeTemplate: "Different/{title}/{episode_key}.{extension}"})
			if _, err := db.New(f.pool).SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: rules}); err != nil {
				t.Fatal(err)
			}
			receipt, raw := f.queue(t)
			// Neither newly saved rules nor an unavailable current configuration may
			// replace the path in a separate, immutable explicit confirmation.
			if _, err := db.New(f.pool).SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			f.start(t)
			f.completed(t, receipt)
			f.stop(t)
			f.verifyImport(t, receipt, action)
			f.recoverOffline(t, receipt, raw, "completed")
			if history != f.confirmationHistory(t) {
				t.Fatal("template configuration changed original identity/target confirmation")
			}
			if _, err := os.Stat(filepath.Join(f.target, "Different")); !os.IsNotExist(err) {
				t.Fatal("template replaced the explicitly confirmed target", err)
			}
			f.log(t, "m9_explicit_target_"+action, 1)
		})
	}
}
