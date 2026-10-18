package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestOwnRequirementWithdrawalScopeAndAtomicReceipt(t *testing.T) {
	for _, physical := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsent", true: "unknown"}[physical], func(t *testing.T) {
			a, admin, member, root, unit, candidate, router, _ := setupOperationHTTP(t)
			ctx := context.Background()
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
			created := call(member, "POST", "/api/v1/movies/acquire", "owner-acq", httpapi.AcquireMovieRequest{RootId: root, UnitId: unit, CandidateId: candidate})
			var op httpapi.OperationDetails
			if created.Code != 202 || json.Unmarshal(created.Body.Bytes(), &op) != nil {
				t.Fatal(created.Code)
			}
			path := "/api/v1/operations/" + op.OperationId.String() + "/requirements"
			listed := call(member, "GET", path, "", nil)
			var own httpapi.ListOwnOperationRequirements200JSONResponse
			if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &own) != nil || len(own.Items) != 1 || len(own.Items[0].FenceDigest) != 64 {
				t.Fatal("own list", listed.Code)
			}
			target := own.Items[0]
			if r := call(admin, "GET", path, "", nil); r.Code != 403 {
				t.Fatal("unrelated admin received owner's requirement", r.Code)
			}
			if physical {
				client := uuid.New()
				if _, e := a.Queries().CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "fixture", ClientType: "qbittorrent", Url: "https://fixture.invalid", PasswordEncrypted: []byte("sealed"), RemotePathMap: []byte(`{}`)}); e != nil {
					t.Fatal(e)
				}
				if _, e := a.Queries().CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: uuid.New(), OperationID: op.OperationId, ClientID: client, CandidateID: candidate, State: "unknown", SavePath: "/fixture"}); e != nil {
					t.Fatal(e)
				}
			}
			action := path + "/" + target.Id.String() + "/withdraw"
			body := map[string]string{"expected_fence": target.FenceDigest}
			if r := call(admin, "POST", action, "withdraw", body); r.Code != 403 {
				t.Fatal("admin implicitly forced foreign withdrawal", r.Code)
			}
			if r := call(member, "POST", action, "bad-fence", map[string]string{"expected_fence": strings.Repeat("0", 64)}); r.Code != 409 {
				t.Fatal("stale source authority", r.Code)
			}
			if _, e := a.DBPool().Exec(ctx, "CREATE FUNCTION reject_withdraw_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='withdraw' THEN RAISE EXCEPTION 'fixture-secret'; END IF;RETURN NEW;END $$;CREATE TRIGGER reject_withdraw_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_withdraw_receipt()"); e != nil {
				t.Fatal(e)
			}
			if r := call(member, "POST", action, "withdraw", body); r.Code != 503 || bytes.Contains(r.Body.Bytes(), []byte("fixture-secret")) {
				t.Fatal("partial or leaking withdrawal receipt", r.Code)
			}
			rows, e := a.Queries().ListRequirementsForOperation(ctx, op.OperationId)
			if e != nil || rows[0].State != "pending" {
				t.Fatal("failed receipt partially withdrew", rows, e)
			}
			if _, e = a.DBPool().Exec(ctx, "DROP TRIGGER reject_withdraw_receipt ON idempotency_record"); e != nil {
				t.Fatal(e)
			}
			outputs := make(chan *httptest.ResponseRecorder, 8)
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() { defer wg.Done(); outputs <- call(member, "POST", action, "withdraw", body) }()
			}
			wg.Wait()
			close(outputs)
			for r := range outputs {
				var receipt httpapi.RequirementWithdrawalReceipt
				if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &receipt) != nil || receipt.PhysicalReleased == physical {
					t.Fatal("unstable physical-recall promise", r.Code, receipt)
				}
			}
			var active int
			if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND state IN ('reserved','dispatched')", op.OperationId).Scan(&active); e != nil || active != map[bool]int{false: 0, true: 1}[physical] {
				t.Fatal("wrong protection", active, e)
			}
			if r := call(admin, "POST", action, "withdraw", body); r.Code != 403 {
				t.Fatal("replay elevated scope", r.Code)
			}
			if r := call(member, "POST", action, "withdraw", map[string]string{"expected_fence": strings.Repeat("0", 64)}); r.Code != 409 {
				t.Fatal("fingerprint changed", r.Code)
			}
		})
	}
}
