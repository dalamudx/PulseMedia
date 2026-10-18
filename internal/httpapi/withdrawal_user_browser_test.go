package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestRequirementWithdrawalCurrentUserBrowser(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	a, _, member, root, unit, candidate, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	body, _ := json.Marshal(httpapi.AcquireMovieRequest{RootId: root, UnitId: unit, CandidateId: candidate})
	request := httptest.NewRequest(http.MethodPost, "http://fixture.test/api/v1/movies/acquire", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+member)
	request.Header.Set("Idempotency-Key", "withdraw-browser-acquire")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var op httpapi.OperationDetails
	if response.Code != 202 || json.Unmarshal(response.Body.Bytes(), &op) != nil {
		t.Fatal("acquisition setup", response.Code)
	}
	rows, e := a.Queries().ListRequirementsForOperation(ctx, op.OperationId)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
	own := rows[0]
	client := uuid.New()
	if _, e = a.Queries().CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "Owned browser client", ClientType: "qbittorrent", Url: "https://fixture.invalid", PasswordEncrypted: []byte("sealed"), RemotePathMap: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Queries().CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: uuid.New(), OperationID: op.OperationId, ClientID: client, CandidateID: candidate, State: "unknown", SavePath: "/fixture"}); e != nil {
		t.Fatal(e)
	}
	other := uuid.New()
	if _, e = a.Queries().CreateUser(ctx, db.CreateUserParams{ID: other, Username: "other-owner", PasswordHash: "fixture", Role: "member", IsActive: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DBPool().Exec(ctx, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,submission_fence) VALUES($1,$2,$3,$4,'user',$5,'{}')`, uuid.New(), op.OperationId, unit, root, other.String()); e != nil {
		t.Fatal(e)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture/disable", "/fixture/restore":
			if _, e := a.DBPool().Exec(r.Context(), "UPDATE app_user SET is_active=$1 WHERE id=$2", r.URL.Path == "/fixture/restore", uuid.MustParse(own.ActorID)); e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(200)
		case "/fixture/counts":
			var state string
			var protected, receipts, foreign int
			if e := a.DBPool().QueryRow(r.Context(), `SELECT (SELECT state FROM download_requirement WHERE id=$1),(SELECT count(*) FROM unit_reservation WHERE operation_id=$2 AND state IN ('reserved','dispatched')),(SELECT count(*) FROM idempotency_record WHERE response_code=200),(SELECT count(*) FROM download_requirement WHERE operation_id=$2 AND actor_id=$3 AND state='pending')`, own.ID, op.OperationId, other.String()).Scan(&state, &protected, &receipts, &foreign); e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if e := json.NewEncoder(w).Encode(map[string]any{"state": state, "protected": protected, "receipts": receipts, "foreign": foreign}); e != nil {
				t.Error(e)
			}
		default:
			router.ServeHTTP(w, r)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 85*time.Second)
	defer cancel()
	command := exec.CommandContext(browserCtx, "node", "../../web/tests/withdrawal-user-browser.mjs")
	command.Env = append(os.Environ(), "PULSE_WITHDRAW_BASE="+server.URL, "PULSE_WITHDRAW_SESSION="+member, "PULSE_WITHDRAW_OPERATION="+op.OperationId.String(), "PULSE_WITHDRAW_REQUIREMENT="+own.ID.String())
	output, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("withdraw browser: %v %s", e, output)
	}
	t.Log(string(output))
}
