package settings_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/jackc/pgx/v5/pgxpool"
)

func namingPreviewSnapshot(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"setting", "idempotency_record", "audit_log", "outbox_event", "job", "download_task", "transfer_plan", "transfer_step", "library_file", "unit_fact"} {
		var snapshot string
		if err := pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text),'[]'::jsonb)::text FROM `+table+` row`).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		out[table] = snapshot
	}
	return out
}

func requireNamingPreviewSnapshot(t *testing.T, pool *pgxpool.Pool, before map[string]string) {
	t.Helper()
	after := namingPreviewSnapshot(t, pool)
	for table, rows := range before {
		if after[table] != rows {
			t.Fatalf("preview changed %s", table)
		}
	}
}

func TestNamingPreviewSavedDraftAndReadOnlyPG(t *testing.T) {
	pool, service, _, admin, _ := setupSettingsPoolTest(t)
	ctx := t.Context()
	input := naming.MediaTemplateInput{Title: "Fixture", Year: 2024, Extension: ".mkv", Season: 1, Episode: 7}
	before := namingPreviewSnapshot(t, pool)
	defaults, err := service.PreviewNaming(ctx, admin, settings.NamingPreviewRequest{MediaType: "movie", Input: input})
	if err != nil || defaults.Draft || defaults.Version != 0 || defaults.RelativePath != "Fixture (2024)/Fixture (2024).mkv" {
		t.Fatal("default preview inaccurate", err)
	}
	requireNamingPreviewSnapshot(t, pool, before)
	rules := naming.TemplateRules{MovieTemplate: "Movies/{title} ({year})/feature.{extension}", EpisodeTemplate: "TV/{title}/Season {season}/{episode_key}.{extension}"}
	if _, err = service.UpdateSetting(ctx, admin, naming.TemplateSettingKey, settings.UpdateSettingRequest{Value: rules}, "preview-config"); err != nil {
		t.Fatal(err)
	}
	before = namingPreviewSnapshot(t, pool)
	for _, test := range []struct{ mediaType, expected string }{{"movie", "Movies/Fixture (2024)/feature.mkv"}, {"tv", "TV/Fixture/Season 01/S01E07.mkv"}} {
		result, err := service.PreviewNaming(ctx, admin, settings.NamingPreviewRequest{MediaType: test.mediaType, Input: input})
		if err != nil || result.RelativePath != test.expected || result.Draft || result.Version != 1 {
			t.Fatal("saved preview inaccurate", err)
		}
	}
	draft := naming.TemplateRules{MovieTemplate: "Draft/{title}.{extension}"}
	result, err := service.PreviewNaming(ctx, admin, settings.NamingPreviewRequest{MediaType: "movie", Input: input, Rules: &draft})
	if err != nil || !result.Draft || result.Version != 1 || result.RelativePath != "Draft/Fixture.mkv" {
		t.Fatal("draft changed saved configuration", err)
	}
	missing := input
	missing.Year = 0
	if _, err = service.PreviewNaming(ctx, admin, settings.NamingPreviewRequest{MediaType: "movie", Input: missing}); !errors.Is(err, ports.ErrInvalidSettingCommand) {
		t.Fatal("unknown year invented", err)
	}
	input.Episode = 0
	if _, err = service.PreviewNaming(ctx, admin, settings.NamingPreviewRequest{MediaType: "tv", Input: input}); !errors.Is(err, ports.ErrInvalidSettingCommand) {
		t.Fatal("unknown episode invented", err)
	}
	requireNamingPreviewSnapshot(t, pool, before)
}

func TestNamingPreviewCurrentPermissionPG(t *testing.T) {
	for _, change := range []string{"member", "token", "demoted", "inactive"} {
		t.Run(change, func(t *testing.T) {
			pool, service, _, admin, member := setupSettingsPoolTest(t)
			actor := admin
			switch change {
			case "member":
				actor = member
			case "token":
				copy := *admin
				copy.Scopes = []string{"read", "write"}
				actor = &copy
			case "demoted":
				if _, err := pool.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, admin.ID); err != nil {
					t.Fatal(err)
				}
			case "inactive":
				if _, err := pool.Exec(t.Context(), `UPDATE app_user SET is_active=false WHERE id=$1`, admin.ID); err != nil {
					t.Fatal(err)
				}
			}
			before := namingPreviewSnapshot(t, pool)
			_, err := service.PreviewNaming(t.Context(), actor, settings.NamingPreviewRequest{MediaType: "movie", Input: naming.MediaTemplateInput{Title: "Fixture", Extension: ".mkv"}})
			if !errors.Is(err, ports.ErrForbidden) {
				t.Fatal("stale authority previewed naming configuration", err)
			}
			requireNamingPreviewSnapshot(t, pool, before)
		})
	}
}

func TestNamingPreviewUnavailableDoesNotFallbackPG(t *testing.T) {
	pool, service, _, admin, _ := setupSettingsPoolTest(t)
	ctx := t.Context()
	_, err := pool.Exec(ctx, `INSERT INTO setting(key,value,version) VALUES ('organization.naming_templates','{}'::jsonb,1)`)
	if err != nil {
		t.Fatal(err)
	}
	before := namingPreviewSnapshot(t, pool)
	request := settings.NamingPreviewRequest{MediaType: "movie", Input: naming.MediaTemplateInput{Title: "Fixture", Extension: ".mkv"}}
	if _, err = service.PreviewNaming(ctx, admin, request); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("malformed stored templates silently became defaults", err)
	}
	requireNamingPreviewSnapshot(t, pool, before)
	if _, err = pool.Exec(ctx, `ALTER TABLE setting RENAME TO fixture_naming_setting_unavailable`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `ALTER TABLE fixture_naming_setting_unavailable RENAME TO setting`); err != nil {
			t.Error(err)
		}
	})
	request.Rules = &naming.TemplateRules{}
	if _, err = service.PreviewNaming(ctx, admin, request); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("draft bypassed unavailable configuration/permission snapshot", err)
	}
}
