package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"net/http/httptest"
	"testing"
)

func TestRootSelectionsOnlyReferencesAndWrongTypeCannotSubscribe(t *testing.T) {
	a, _, member, root, unit, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+member)
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	listed := request("GET", "/api/v1/library-roots", nil)
	var roots httpapi.ListLibraryRootSelections200JSONResponse
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &roots) != nil || len(roots.Items) != 1 || roots.Items[0].Id != root {
		t.Fatal("root selection missing", listed.Code)
	}
	if bytes.Contains(listed.Body.Bytes(), []byte("path")) {
		t.Fatal("root filesystem path leaked")
	}
	wrong := uuid.New()
	if _, e := a.Queries().CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: wrong, Path: t.TempDir(), Name: "TV", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	u, e := a.Queries().GetSearchUnit(ctx, unit)
	if e != nil {
		t.Fatal(e)
	}
	r := request("POST", "/api/v1/subscriptions", httpapi.CreateSubscriptionRequest{MediaId: &u.MediaID, RootId: &wrong})
	if r.Code == 201 {
		t.Fatal("movie subscribed to TV root")
	}
	var count int
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM subscription WHERE root_id=$1", wrong).Scan(&count); e != nil || count != 0 {
		t.Fatal("wrong-type subscription partially committed", count, e)
	}
	anon := httptest.NewRecorder()
	router.ServeHTTP(anon, httptest.NewRequest("GET", "/api/v1/library-roots", nil))
	if anon.Code != 401 {
		t.Fatal("anonymous root references accessible", anon.Code)
	}
}
