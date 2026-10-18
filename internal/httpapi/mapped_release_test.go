package httpapi_test

import (
	"bytes"
	"context"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMappedReleaseHTTPAuthorizationAndNoEffects(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(h *httpapi.ServerHandler, a *app.App) {
		source := postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(a.DBPool(), idgen.New()))
		h.SetEpisodeMappingService(subscription.NewEpisodeMappingService(postgres.NewEpisodeMappingStore(source, idgen.New()), subscription.NewEpisodeSourceService(source, nil)))
	})
	path := "/api/v1/subscriptions/" + uuid.NewString() + "/episode-mapping/explain-release"
	for _, c := range []struct {
		token, body string
		cookie      bool
		status      int
	}{{"", `{"name":"[Team] Example - 25 [1080p].mkv"}`, false, 401}, {member, `{"name":"[Team] Example - 25 [1080p].mkv"}`, false, 403}, {admin, `{"name":"[Team] Example - 25 [1080p].mkv"}`, true, 403}, {admin, `{"name":"https://example.test/?passkey=private"}`, false, 400}, {admin, `{"name":""}`, false, 400}, {admin, `{"name":"[Team] Example - 25 [1080p].mkv"}`, false, 404}} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(c.body))
		req.Header.Set("Content-Type", "application/json")
		if c.cookie {
			req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: c.token})
		} else if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != c.status || strings.Contains(w.Body.String(), "passkey") {
			t.Fatal("unsafe response", w.Code, w.Body.String())
		}
	}
	var work int
	if e := a.DBPool().QueryRow(context.Background(), "SELECT (SELECT count(*) FROM unit_reservation)+(SELECT count(*) FROM job WHERE kind='download_submission')").Scan(&work); e != nil || work != 0 {
		t.Fatal("explanation side effects", work, e)
	}
}
