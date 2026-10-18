package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	mediausecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFileMappingHTTPHistoryAndRecovery(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(s *httpapi.ServerHandler, a *app.App) {
		s.SetFileMappingService(mediausecase.NewFileMappingService(postgres.NewFileMappingStore(a.DBPool(), idgen.New())))
	})
	ctx := context.Background()
	q := a.Queries()
	root, e := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Name: "UI mapping root", Path: t.TempDir(), MediaType: "tv"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", root.ID); e != nil {
		t.Fatal(e)
	}
	m, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), Title: "UI Mapping Show", MediaType: "tv"})
	if e != nil {
		t.Fatal(e)
	}
	units := []uuid.UUID{}
	for ep := 1; ep <= 3; ep++ {
		u, e := q.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: uuid.New(), MediaID: m.ID, UnitKey: fmt.Sprintf("S01E%02d", ep), SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: int32(ep), Valid: true}})
		if e != nil {
			t.Fatal(e)
		}
		units = append(units, u.ID)
	}
	file, e := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: root.ID, RelPath: "UI.Mapping.S01E01E03.mkv", SizeBytes: 4096, MtimeNs: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: units[1]}); e != nil {
		t.Fatal(e)
	}
	fact, e := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: units[1], Stage: "imported", RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","source":"WEB-DL"}`)})
	if e != nil {
		t.Fatal(e)
	}
	call := func(token, method, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		return out
	}
	path := "/api/v1/library/files/" + file.ID.String() + "/mapping"
	previewPath := path + "?media_id=" + m.ID.String()
	options := call(admin, "GET", "/api/v1/library/mapping-options", "", nil)
	if options.Code != 200 || bytes.Contains(options.Body.Bytes(), []byte(root.Path)) {
		t.Fatal("absolute root leaked", options.Code)
	}
	if out := call(member, "GET", previewPath, "", nil); out.Code != 403 {
		t.Fatal("member object access", out.Code)
	}
	read := call(admin, "GET", previewPath, "", nil)
	var p ports.FileMappingPreview
	if read.Code != 200 || read.Header().Get("Cache-Control") != "private, no-store" || json.Unmarshal(read.Body.Bytes(), &p) != nil || !p.CanCorrect {
		t.Fatal("valid preview unavailable", read.Code, read.Body.String())
	}
	body := ports.FileMappingCommand{MediaID: m.ID, UnitIDs: []uuid.UUID{units[0], units[2]}, ExpectedStamp: p.InputStamp, ConfirmedCoverage: true}
	if os.Getenv("PULSE_UI_FIXTURE") == "1" {
		server := httptest.NewServer(router)
		defer server.Close()
		work, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		script, e := filepath.Abs("../../web/tests/file-mapping-browser.mjs")
		if e != nil {
			t.Fatal(e)
		}
		cdp, stopBrowser := startFileMappingBrowser(t, work)
		defer stopBrowser()
		cmd := exec.CommandContext(work, "node", script)
		cmd.Env = append(os.Environ(), "PULSE_UI_FIXTURE=1", "PULSE_MAPPING_UI_CDP="+cdp, "PULSE_MAPPING_UI_BASE="+server.URL, "PULSE_MAPPING_UI_FILE="+file.ID.String(), "PULSE_MAPPING_UI_MEDIA="+m.ID.String())
		raw, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("mapping browser failed: %v %s", e, raw)
		}
		t.Log(string(raw))
	} else {
		post := call(admin, "POST", path, "mapping-http", body)
		if post.Code != 200 {
			t.Fatal(post.Code, post.Body.String())
		}
		again := call(admin, "POST", path, "mapping-http", body)
		if again.Code != 200 || again.Body.String() != post.Body.String() {
			t.Fatal("unstable recovery", again.Code)
		}
		if out := call(admin, "POST", path, "mapping-stale", body); out.Code != 409 {
			t.Fatal("old source stamp accepted", out.Code)
		}
		exerciseFileMappingAuthority(t, a, file.ID, body)
	}
	var revoked bool
	if e = a.DBPool().QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM unit_fact WHERE id=$1", fact.ID).Scan(&revoked); e != nil || !revoked {
		t.Fatal("old imported source revived", e)
	}
	history, e := q.ListFileMappingHistory(ctx, file.ID)
	if e != nil || len(history) != 3 {
		t.Fatal("history not preserved", history, e)
	}
	active, e := q.ListUnitsForFile(ctx, file.ID)
	if e != nil || len(active) != 2 {
		t.Fatal("incorrect active coverage", active, e)
	}
	seen := map[uuid.UUID]bool{}
	for _, u := range active {
		seen[u.ID] = true
	}
	if !seen[units[0]] || !seen[units[2]] || seen[units[1]] {
		t.Fatal("explicit E01/E03 expanded E02")
	}
	if out := call(member, "POST", path, "mapping-http", body); out.Code != 403 {
		t.Fatal("cached role elevation", out.Code)
	}
}
