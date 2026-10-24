package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
)

func TestM9NamingPreviewCookieHTTPPG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	const path = "/api/v1/organization/naming/preview"
	const setting = "/api/v1/settings/organization.naming_templates"
	call := func(method, url, key string, body any, session string, status int) m5IndexerHTTPResult {
		t.Helper()
		result := f.request(t, method, url, key, body, session)
		m5IndexerStatus(t, result, status)
		if result.header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("naming response is cacheable", status)
		}
		return result
	}
	check := func(body any, expected string, version int64, draft bool) {
		t.Helper()
		result := call(http.MethodPost, path, "", body, f.session, http.StatusOK)
		var page httpapi.NamingPreviewResult
		if json.Unmarshal(result.body, &page) != nil || page.RelativePath != expected || page.ConfigurationVersion != version || page.Draft != draft || page.ExecutionAllowed {
			t.Fatal("incorrect preview or execution authority")
		}
	}
	rules := naming.TemplateRules{MovieTemplate: "Films/{title} ({year}) [{resolution}]/{title}.{extension}", EpisodeTemplate: "Series/{title}/Season {season}/{episode_key}.{extension}"}
	input := map[string]any{"media_type": "movie", "title": "Native trusted movie", "year": 1999, "extension": "mkv", "release_title": "Native.Trusted.Movie.1999.1080p.WEB-DL.x264-GROUP"}
	before := m9WordHTTPStable(t, f)
	metadataBefore := metadata.count()
	nativeBefore := f.calls.Load()
	check(input, "Native trusted movie (1999)/Native trusted movie (1999) [WEB-DL 1080p H264].mkv", 0, false)
	if !reflect.DeepEqual(before, m9WordHTTPState(t, f)) || metadata.count() != metadataBefore || f.calls.Load() != nativeBefore {
		t.Fatal("default preview wrote state or probed a provider")
	}
	saveBody := map[string]any{"expected_version": 0, "value": rules}
	saved := call(http.MethodPut, setting, "naming-public-original", saveBody, f.session, http.StatusOK)
	before = m9WordHTTPStable(t, f)
	check(input, "Films/Native trusted movie (1999) [1080p]/Native trusted movie.mkv", 1, false)
	tv := map[string]any{"media_type": "tv", "title": "Sparse show", "season": 1, "episode": 7, "extension": "mkv"}
	check(tv, "Series/Sparse show/Season 01/S01E07.mkv", 1, false)
	draft := map[string]any{"media_type": "movie", "title": "Fixture {year}", "extension": "mkv", "templates": naming.TemplateRules{MovieTemplate: "Draft/{title}.{extension}"}}
	check(draft, "Draft/Fixture {year}.mkv", 1, true)
	for _, body := range []any{
		map[string]any{"media_type": "movie", "title": "Fixture", "extension": "mkv"},
		map[string]any{"media_type": "tv", "title": "Sparse show", "season": 1, "extension": "mkv"},
		map[string]any{"media_type": "movie", "title": "Fixture", "extension": "mkv", "templates": naming.TemplateRules{MovieTemplate: "../{title}.{extension}"}},
		map[string]any{"media_type": "movie", "title": "Fixture", "extension": "mp4", "templates": naming.TemplateRules{MovieTemplate: "{title}.mkv"}},
		`{"media_type":"movie","title":"Fixture","title":"Other","extension":"mkv"}`,
		map[string]any{"media_type": "movie", "title": "Fixture", "extension": "mkv", "release_title": strings.Repeat("x", 1025)},
	} {
		call(http.MethodPost, path, "", body, f.session, http.StatusBadRequest)
	}
	for _, body := range []any{
		`{"expected_version":1,"value":{"movie_template":"","movie_template":"{title}.mkv","episode_template":""}}`,
		map[string]any{"expected_version": 1, "value": map[string]any{"Movie_template": "", "episode_template": ""}},
		map[string]any{"expected_version": 1, "value": naming.TemplateRules{MovieTemplate: "{episode_key}.mkv"}},
	} {
		call(http.MethodPut, setting, "naming-public-invalid", body, f.session, http.StatusBadRequest)
	}
	replay := call(http.MethodPut, setting, "naming-public-original", saveBody, f.session, http.StatusOK)
	receipt := call(http.MethodGet, setting+"/commands/naming-public-original", "", nil, f.session, http.StatusOK)
	if !bytes.Equal(saved.body, replay.body) || !bytes.Equal(saved.body, receipt.body) {
		t.Fatal("original naming receipt changed")
	}
	if !reflect.DeepEqual(before, m9WordHTTPState(t, f)) || metadata.count() != metadataBefore || f.calls.Load() != nativeBefore {
		t.Fatal("preview/replay/invalid request changed protected data or requested upstream")
	}
	// Clearing affects only new plans. The old command remains byte-identical.
	call(http.MethodPut, setting, "naming-public-clear", map[string]any{"expected_version": 1, "value": naming.TemplateRules{}}, f.session, http.StatusOK)
	before = m9WordHTTPStable(t, f)
	check(input, "Native trusted movie (1999)/Native trusted movie (1999) [WEB-DL 1080p H264].mkv", 2, false)
	replay = call(http.MethodPut, setting, "naming-public-original", saveBody, f.session, http.StatusOK)
	if !bytes.Equal(saved.body, replay.body) {
		t.Fatal("reset changed historical naming receipt")
	}
	call(http.MethodPut, setting, "naming-public-stale", saveBody, f.session, http.StatusPreconditionFailed)
	call(http.MethodPost, path, "", input, "", http.StatusUnauthorized)
	if _, err := f.app.DBPool().Exec(t.Context(), `UPDATE app_user SET role='member' WHERE username='creation-admin'`); err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, path, "", input, f.session, http.StatusForbidden)
	call(http.MethodPut, setting, "naming-public-original", saveBody, f.session, http.StatusForbidden)
	if !reflect.DeepEqual(before, m9WordHTTPState(t, f)) || metadata.count() != metadataBefore || f.calls.Load() != nativeBefore {
		t.Fatal("stale/denied requests changed protected data or probed upstream")
	}
	if _, err := f.app.DBPool().Exec(t.Context(), `UPDATE app_user SET role='admin' WHERE username='creation-admin'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.DBPool().Exec(t.Context(), `ALTER TABLE setting RENAME TO fixture_naming_http_unavailable`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.app.DBPool().Exec(ctx, `ALTER TABLE fixture_naming_http_unavailable RENAME TO setting`); err != nil {
			t.Error(err)
		}
	})
	call(http.MethodPost, path, "", input, f.session, http.StatusServiceUnavailable)
	// Original-key restoration precedes current configuration reads, even offline.
	replay = call(http.MethodPut, setting, "naming-public-original", saveBody, f.session, http.StatusOK)
	if !bytes.Equal(saved.body, replay.body) || metadata.count() != metadataBefore || f.calls.Load() != nativeBefore {
		t.Fatal("offline receipt recovery performed extra work")
	}
	t.Log("M9_NAMING_COOKIE_HTTP_PASS Cookie=REAL App=REAL PG=18.6 draft_saved_default=true sparse_episode=S01E07 relative_only=true execution_allowed=false full_non_auth_rows=UNCHANGED reads_and_replay upstream_calls=0 original_receipt=BYTE_IDENTICAL offline_restore=true")
}
