package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	mediausecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/google/uuid"
)

func TestFileMappingOptionsActualRouterPG(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(s *httpapi.ServerHandler, a *app.App) {
		s.SetFileMappingService(mediausecase.NewFileMappingService(postgres.NewFileMappingStore(a.DBPool(), idgen.New())))
	})
	root, err := a.Queries().CreateLibraryRoot(t.Context(), db.CreateLibraryRootParams{ID: uuid.New(), Name: "Target", Path: t.TempDir(), MediaType: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.DBPool().Exec(t.Context(), `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,created_at) SELECT md5('http-page-'||n::text)::uuid,$1,'Show.'||n||'.mkv',100,1,'2026-01-01T00:00:00Z'::timestamptz FROM generate_series(1,101) n`, root.ID); err != nil {
		t.Fatal(err)
	}
	call := func(cookie, suffix string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/library/mapping-options"+suffix, nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("options response was cacheable", w.Code)
		}
		return w
	}
	first := call(admin, "?root_id="+root.ID.String())
	var page ports.FileMappingOptions
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Files) != 100 || !page.FilesHasMore || bytes.Contains(first.Body.Bytes(), []byte(root.Path)) {
		t.Fatal("real cookie first page", first.Code)
	}
	second := call(admin, "?root_id="+root.ID.String()+"&cursor="+page.FilesNextCursor)
	var next ports.FileMappingOptions
	if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &next) != nil || len(next.Files) != 1 || next.FilesHasMore {
		t.Fatal("real cookie continuation", second.Code)
	}
	for _, suffix := range []string{"?root_id=" + root.ID.String() + "&root_id=" + root.ID.String(), "?cursor=not-a-cursor", "?unexpected=true"} {
		if got := call(admin, suffix); got.Code != 400 {
			t.Fatal("invalid query reached options", got.Code)
		}
	}
	if got := call(member, ""); got.Code != 403 {
		t.Fatal("member options access", got.Code)
	}
	if got := call("", ""); got.Code != 401 {
		t.Fatal("anonymous options access", got.Code)
	}
	if _, err = a.DBPool().Exec(t.Context(), `UPDATE app_user SET role='member' WHERE username='admin'`); err != nil {
		t.Fatal(err)
	}
	if got := call(admin, "?root_id="+root.ID.String()+"&cursor="+page.FilesNextCursor); got.Code != 403 {
		t.Fatal("demoted administrator continued pagination", got.Code)
	}
}
