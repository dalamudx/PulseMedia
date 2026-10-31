package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadcase "github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
	"net/http/httptest"
	"strings"
	"testing"
)

type controlHTTPRepo struct {
	reads, sends, observes int
	receipt                ports.DownloadControlReceipt
	failure                error
}

func (r *controlHTTPRepo) GetDownloadControl(context.Context, *ports.Actor, uuid.UUID, string) (*ports.DownloadControlReceipt, error) {
	r.reads++
	return &r.receipt, r.failure
}
func (r *controlHTTPRepo) PrepareDownloadControl(_ context.Context, _ *ports.Actor, _ uuid.UUID, _ string, _ ports.DownloadControlCommand, observe bool) (*ports.PreparedDownloadControl, error) {
	if observe {
		r.observes++
	} else {
		r.sends++
	}
	return &ports.PreparedDownloadControl{Receipt: &r.receipt}, r.failure
}
func TestDownloadControlStrictHTTPDispatch(t *testing.T) {
	op := uuid.New()
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	repo := &controlHTTPRepo{receipt: ports.DownloadControlReceipt{CommandID: uuid.New(), OperationID: op, Action: "pause", State: "unknown"}}
	s := &ServerHandler{}
	s.SetDownloadControlService(downloadcase.NewControlService(repo))
	h := StrictDownloadControlJSON(HandlerWithOptions(NewStrictHandler(s, nil), StdHTTPServerOptions{}))
	call := func(method, suffix, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/operations/"+op.String()+"/download-control"+suffix, strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "original")
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), actorKey, actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	for _, command := range []struct{ method, suffix, body string }{{"GET", "", ""}, {"POST", "", `{"action":"pause","confirmed":true}`}, {"POST", "/observations", ""}} {
		w := call(command.method, command.suffix, command.body)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(w.Code, w.Body.String())
		}
		var got ports.DownloadControlReceipt
		if json.Unmarshal(w.Body.Bytes(), &got) != nil || got != repo.receipt {
			t.Fatal("receipt changed")
		}
	}
	if repo.reads != 1 || repo.sends != 1 || repo.observes != 1 {
		t.Fatal("GET executed native preparation", repo)
	}
	actor.Scopes = []string{"admin"}
	if w := call("POST", "", `{"action":"pause","confirmed":true}`); w.Code != 403 {
		t.Fatal("token gained authority", w.Code)
	}
	actor.Scopes = nil
	repo.failure = errors.New("private endpoint secret-passkey")
	w := call("GET", "", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret-passkey") {
		t.Fatal("private native error exposed", w.Code, w.Body.String())
	}
}
