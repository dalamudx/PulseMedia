package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"net/http/httptest"
	"testing"
)

func TestLegacyDownloadWithoutRecognizedUnitCreatesNoGuessedMediaOrWork(t *testing.T) {
	a, admin, _, _, _, candidate, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	counts := func() []int {
		var m, u, r, j int
		if e := a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM media),(SELECT count(*) FROM media_unit),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM job WHERE kind='download_submission')").Scan(&m, &u, &r, &j); e != nil {
			t.Fatal(e)
		}
		return []int{m, u, r, j}
	}
	before, _ := json.Marshal(counts())
	raw, _ := json.Marshal(httpapi.DownloadReleaseRequest{CandidateId: candidate})
	request := httptest.NewRequest("POST", "/api/v1/releases/download", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+admin)
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, request)
	if w.Code != 400 || !bytes.Contains(w.Body.Bytes(), []byte("UNIT_REQUIRED")) {
		t.Fatal("legacy guessed target persisted", w.Code, w.Body.String())
	}
	after, _ := json.Marshal(counts())
	if !bytes.Equal(before, after) {
		t.Fatal("rejected request partially created media/unit/reservation/job", string(before), string(after))
	}
}
