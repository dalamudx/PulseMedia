package settings_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/jackc/pgx/v5/pgxpool"
)

func namingSettingCounts(t *testing.T, pool *pgxpool.Pool) [7]int {
	t.Helper()
	var counts [7]int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM setting WHERE key=$1),(SELECT count(*) FROM idempotency_record),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM job),(SELECT count(*) FROM download_task),(SELECT count(*) FROM transfer_plan)`, naming.TemplateSettingKey).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6]); err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestNamingTemplateSettingsConcurrentReceiptsPG(t *testing.T) {
	pool, service, _, admin, _ := setupSettingsPoolTest(t)
	ctx := t.Context()
	rules := naming.TemplateRules{MovieTemplate: "{title} ({year})/{title}.{extension}", EpisodeTemplate: "{title}/Season {season}/{episode_key}.{extension}"}
	request := settings.UpdateSettingRequest{Value: rules}
	start := make(chan struct{})
	results := make(chan *ports.SettingCommandResult, 8)
	failures := make(chan error, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := service.UpdateSetting(ctx, admin, naming.TemplateSettingKey, request, "naming-original")
			results <- result
			failures <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original *ports.SettingItem
	cacheHits := 0
	for result := range results {
		if result == nil || result.Data == nil || result.Data.Version != 1 {
			t.Fatal("invalid configuration receipt")
		}
		if result.FromCache {
			cacheHits++
		} else {
			original = result.Data
		}
	}
	if cacheHits != 7 || original == nil || namingSettingCounts(t, pool) != ([7]int{1, 1, 1, 1, 0, 0, 0}) {
		t.Fatal("concurrent save duplicated records or started organization")
	}
	got, err := service.GetSetting(ctx, naming.TemplateSettingKey)
	if err != nil || got == nil || got.Version != 1 {
		t.Fatal("current naming configuration missing", err)
	}
	raw, err := json.Marshal(got.Value)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := naming.DecodeTemplateRules(raw)
	if err != nil || stored != rules {
		t.Fatal("configuration did not round trip", err)
	}
	changed := settings.UpdateSettingRequest{Value: naming.TemplateRules{MovieTemplate: "../bad", EpisodeTemplate: ""}}
	if _, err = service.UpdateSetting(ctx, admin, naming.TemplateSettingKey, changed, "naming-original"); !errors.Is(err, ports.ErrCommandConflict) {
		t.Fatal("changed original-key input did not retain conflict precedence", err)
	}
	cleared, err := service.UpdateSetting(ctx, admin, naming.TemplateSettingKey, settings.UpdateSettingRequest{ExpectedVersion: 1, Value: naming.TemplateRules{}}, "naming-clear")
	if err != nil || cleared.Data.Version != 2 {
		t.Fatal("explicit default reset failed", err)
	}
	replay, err := service.UpdateSetting(ctx, admin, naming.TemplateSettingKey, request, "naming-original")
	if err != nil || !replay.FromCache || replay.Data.Version != 1 || !replay.Data.UpdatedAt.Equal(original.UpdatedAt) {
		t.Fatal("later reset changed original receipt", err)
	}
	history, err := service.ReadSettingReceipt(ctx, admin, naming.TemplateSettingKey, "naming-original")
	if err != nil || history.Data.Version != 1 || !history.Data.UpdatedAt.Equal(original.UpdatedAt) {
		t.Fatal("historical receipt unavailable", err)
	}
	if _, err = service.UpdateSetting(ctx, admin, naming.TemplateSettingKey, request, "naming-stale"); !errors.Is(err, ports.ErrSettingVersionConflict) {
		t.Fatal("stale configuration overwrote current templates", err)
	}
	if namingSettingCounts(t, pool) != ([7]int{1, 2, 2, 2, 0, 0, 0}) {
		t.Fatal("recovery or stale save changed effects")
	}
}

func TestNamingTemplateSettingsRejectInvalidWithoutWritesPG(t *testing.T) {
	pool, service, _, admin, member := setupSettingsPoolTest(t)
	for _, value := range []any{
		nil, "not an object", map[string]any{"movie_template": ""},
		map[string]any{"Movie_template": "", "episode_template": ""},
		map[string]any{"movie_template": "", "episode_template": "", "extra": true},
		naming.TemplateRules{MovieTemplate: "{title}/../escape.mkv"},
		naming.TemplateRules{MovieTemplate: "{episode_key}.mkv"},
		naming.TemplateRules{EpisodeTemplate: "{missing}.mkv"},
	} {
		if _, err := service.UpdateSetting(t.Context(), admin, naming.TemplateSettingKey, settings.UpdateSettingRequest{Value: value}, "invalid-naming"); !errors.Is(err, ports.ErrInvalidSettingCommand) {
			t.Fatal("invalid new configuration accepted", err)
		}
		if namingSettingCounts(t, pool) != ([7]int{}) {
			t.Fatal("rejected configuration wrote data")
		}
	}
	if _, err := service.UpdateSetting(t.Context(), member, naming.TemplateSettingKey, settings.UpdateSettingRequest{Value: naming.TemplateRules{}}, "member-naming"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("member changed global naming", err)
	}
	if namingSettingCounts(t, pool) != ([7]int{}) {
		t.Fatal("denied member wrote data")
	}
}

func TestNamingTemplateSettingsTailRollbackPG(t *testing.T) {
	pool, service, _, admin, _ := setupSettingsPoolTest(t)
	ctx := t.Context()
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_naming_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.resource_id='organization.naming_templates' THEN RAISE EXCEPTION 'fixture-naming-tail'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_naming_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_naming_receipt()`)
	if err != nil {
		t.Fatal(err)
	}
	request := settings.UpdateSettingRequest{Value: naming.TemplateRules{MovieTemplate: "{title}.{extension}"}}
	if _, err = service.UpdateSetting(ctx, admin, naming.TemplateSettingKey, request, "naming-tail"); err == nil || strings.Contains(err.Error(), "fixture-naming-tail") {
		t.Fatal("tail failure lost error contract")
	}
	if namingSettingCounts(t, pool) != ([7]int{}) {
		t.Fatal("failed tail left partial naming records")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_naming_receipt ON idempotency_record`); err != nil {
		t.Fatal(err)
	}
	saved, err := service.UpdateSetting(ctx, admin, naming.TemplateSettingKey, request, "naming-tail")
	if err != nil || saved.Data.Version != 1 || saved.FromCache || namingSettingCounts(t, pool) != ([7]int{1, 1, 1, 1, 0, 0, 0}) {
		t.Fatal("original-key recovery after rollback failed", err)
	}
}
