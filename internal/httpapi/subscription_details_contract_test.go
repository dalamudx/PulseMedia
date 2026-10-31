package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type detailsContractReader struct{ err error }

func (r detailsContractReader) ReadSubscriptionDetails(context.Context, *ports.Actor, uuid.UUID) (*ports.SubscriptionDetails, error) {
	return nil, r.err
}

func TestSubscriptionDetailsErrorContract(t *testing.T) {
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	ctx := context.WithValue(t.Context(), actorKey, actor)
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"missing", ports.ErrNotFound, 404}, {"forbidden", ports.ErrForbidden, 403}, {"revoked", ports.ErrUnauthorized, 401},
		{"cancelled", context.Canceled, 503}, {"deadline", context.DeadlineExceeded, 503}, {"storage", errors.New("private-storage-sentinel"), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &ServerHandler{subscriptionDetailsReader: detailsContractReader{tc.err}}
			out, err := handler.GetSubscription(ctx, GetSubscriptionRequestObject{Id: uuid.New()})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			if err = out.VisitGetSubscriptionResponse(rec); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status || strings.Contains(rec.Body.String(), "private-storage-sentinel") {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRetiredLegacyEndpointsHaveNoExecutionDependencies(t *testing.T) {
	// No App, database, network adapter or filesystem exists in this handler.
	handler := &ServerHandler{}
	ctx := context.WithValue(t.Context(), actorKey, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	checkin, err := handler.CheckinIndexer(ctx, CheckinIndexerRequestObject{Id: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err = checkin.VisitCheckinIndexerResponse(rec); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 410 || !strings.Contains(rec.Body.String(), "ENDPOINT_RETIRED") {
		t.Fatalf("checkin %d %s", rec.Code, rec.Body.String())
	}
	scan, err := handler.ScanDirectoryReorganize(ctx, ScanDirectoryReorganizeRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	if err = scan.VisitScanDirectoryReorganizeResponse(rec); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 410 || !strings.Contains(rec.Body.String(), "ENDPOINT_RETIRED") {
		t.Fatalf("scan %d %s", rec.Code, rec.Body.String())
	}
	unauth, err := handler.CheckinIndexer(t.Context(), CheckinIndexerRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	_ = unauth.VisitCheckinIndexerResponse(rec)
	if rec.Code != 401 {
		t.Fatal(rec.Code)
	}
}
