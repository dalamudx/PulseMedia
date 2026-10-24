package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/google/uuid"
)

func TestM10EmbyConfirmationNativePG(t *testing.T) {
	f := organizationSetup(t)
	importForOutbox(t, f)
	var file uuid.UUID
	var rel string
	if e := f.pool.QueryRow(t.Context(), `SELECT id,rel_path FROM library_file WHERE root_id=$1 AND retired_at IS NULL`, f.root).Scan(&file, &rel); e != nil {
		t.Fatal(e)
	}
	remote := "/movies/" + filepath.ToSlash(rel)
	var body atomic.Value
	body.Store(`{}`)
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emby/Items" || r.Header.Get("X-Emby-Token") != "private-confirm" || r.URL.Query().Has("Path") {
			t.Error("wrong native query")
		}
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer native.Close()
	cipher, e := crypto.Encrypt(outboxTestKey, []byte("private-confirm"))
	if e != nil {
		t.Fatal(e)
	}
	mapping, _ := json.Marshal(map[string]string{f.target: "/movies"})
	id := uuid.New()
	if _, e = f.q.CreateMediaServer(t.Context(), db.CreateMediaServerParams{ID: id, Name: "Emby", ServerType: "emby", Url: native.URL + "/emby", ApiKeyEncrypted: cipher, PathMapping: mapping}); e != nil {
		t.Fatal(e)
	}
	svc := mediaserver.NewSyncService(nil, postgres.NewMediaConfirmationRepository(f.pool, f.gen))
	svc.SetCredentialDecoder(crypto.NewSecretProtector(outboxTestKey))
	svc.SetServerFactory(NativeMediaServerClient)
	pathJSON, _ := json.Marshal(remote)
	cases := []string{`{}`, `{"Items":null,"TotalRecordCount":0}`, `{"Items":[],"TotalRecordCount":0}`, `{"Items":[{"Id":"other","Path":"/wrong.mkv"}],"TotalRecordCount":1}`, `{"Items":[{"Id":"one","Path":` + string(pathJSON) + `}],"TotalRecordCount":2}`, `{"Items":[{"Id":"one","Path":` + string(pathJSON) + `},{"Id":"two","Path":` + string(pathJSON) + `}],"TotalRecordCount":2}`, `{"Items":[],"items":[],"TotalRecordCount":0}`}
	for _, bad := range cases {
		body.Store(bad)
		if _, e = svc.ConfirmFile(t.Context(), id, file, f.unit); e == nil {
			t.Fatal("unproved file confirmed")
		}
		f.assertImported(t)
	}
	body.Store(`{"Items":[{"Id":"owned","Path":` + string(pathJSON) + `}],"TotalRecordCount":1}`)
	if _, e = f.pool.Exec(t.Context(), `CREATE FUNCTION owned_confirmation_fail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'owned failure';END$$;CREATE TRIGGER owned_confirmation_fail BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION owned_confirmation_fail()`); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ConfirmFile(t.Context(), id, file, f.unit); e == nil {
		t.Fatal("tail rollback accepted")
	}
	if _, e = f.pool.Exec(t.Context(), `DROP TRIGGER owned_confirmation_fail ON audit_log`); e != nil {
		t.Fatal(e)
	}
	f.assertImported(t)
	fact, e := svc.ConfirmFile(t.Context(), id, file, f.unit)
	if e != nil || fact.Stage != "confirmed" || fact.SourceFileID != file {
		t.Fatal("exact proof failed", e)
	}
	var count int
	if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM unit_fact WHERE stage='imported' AND revoked_at IS NULL`).Scan(&count); e != nil || count != 1 {
		t.Fatal("confirmation replaced imported fact", e)
	}
	t.Log("M10_EMBY_CONFIRMATION_PG_PASS native=REAL exact=UNIQUE partial_and_ambiguous=REJECTED imported=PRESERVED tail=FULL_ROLLBACK")
}
