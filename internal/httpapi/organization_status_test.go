package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompletedDownloadIsNotAnOverallCompletedOperation(t *testing.T) {
	a, _, member, root, unit, candidate, router, _ := setupOperationHTTP(t)
	body, _ := json.Marshal(httpapi.AcquireMovieRequest{UnitId: unit, RootId: root, CandidateId: candidate})
	acquire := httptest.NewRequest(http.MethodPost, "/api/v1/movies/acquire", bytes.NewReader(body))
	acquire.Header.Set("Authorization", "Bearer "+member)
	acquire.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, acquire)
	if rec.Code != http.StatusAccepted {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var accepted httpapi.AcquireMovieResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	q := a.Queries()
	ctx := context.Background()
	client := uuid.New()
	if _, err := q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "fixture", ClientType: "qbittorrent", Url: "http://fixture", Username: "fixture", PasswordEncrypted: []byte("fixture"), RemotePathMap: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	task, err := q.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: uuid.New(), OperationID: accepted.OperationId, ClientID: client, CandidateID: candidate, InfoHash: "aaa", State: "completed", SavePath: "/downloads"})
	if err != nil {
		t.Fatal(err)
	}
	for state, want := range map[string]string{"pending": "waiting_organization", "queued": "organizing", "review_required": "review_required"} {
		if _, err := a.DBPool().Exec(ctx, "UPDATE download_task SET organization_state=$2 WHERE id=$1", task.ID, state); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+accepted.OperationId.String(), nil)
		req.Header.Set("Authorization", "Bearer "+member)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		var details httpapi.OperationDetails
		if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil {
			t.Fatal(err)
		}
		if details.Status != want || details.DownloadStatus == nil || *details.DownloadStatus != "completed" {
			t.Fatalf("download completion falsely completed entire operation: %+v", details)
		}
	}
}
