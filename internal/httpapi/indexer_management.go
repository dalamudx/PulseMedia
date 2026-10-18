package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/ports"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

// SetIndexerManagementServices injects the current-account command and receipt
// boundaries assembled by App. HTTP performs no database or cryptographic work.
func (s *ServerHandler) SetIndexerManagementServices(commands *usecaseSearch.IndexerManagementService, receipts *usecaseSearch.IndexerManagementReceiptService) {
	s.indexerManagementSvc, s.indexerReceiptSvc = commands, receipts
}

func (s *ServerHandler) ListIndexers(ctx context.Context, _ ListIndexersRequestObject) (ListIndexersResponseObject, error) {
	if s.indexerManagementSvc == nil {
		return indexerFailure(ctx, ports.ErrUnavailable), nil
	}
	rows, err := s.indexerManagementSvc.List(ctx, GetActor(ctx))
	if err != nil {
		return indexerFailure(ctx, err), nil
	}
	items := make([]IndexerDetails, 0, len(rows))
	for _, row := range rows {
		items = append(items, managedIndexerDetails(row))
	}
	return managedIndexerList{items: items}, nil
}

func (s *ServerHandler) CreateIndexer(ctx context.Context, req CreateIndexerRequestObject) (CreateIndexerResponseObject, error) {
	if req.Body == nil {
		return indexerFailure(ctx, ports.ErrRejected), nil
	}
	if s.indexerManagementSvc == nil {
		return indexerFailure(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.indexerManagementSvc.Create(ctx, GetActor(ctx), ports.IndexerManagementInput{Name: req.Body.Name, Protocol: ports.IndexerProtocol(req.Body.Protocol), URL: req.Body.Url, Credential: req.Body.ApiKey, Categories: req.Body.Categories}, req.Params.IdempotencyKey)
	if err != nil {
		return indexerFailure(ctx, err), nil
	}
	return managedIndexerResult{receipt: receipt}, nil
}

func (s *ServerHandler) UpdateIndexer(ctx context.Context, req UpdateIndexerRequestObject) (UpdateIndexerResponseObject, error) {
	if req.Body == nil {
		return indexerFailure(ctx, ports.ErrRejected), nil
	}
	if s.indexerManagementSvc == nil {
		return indexerFailure(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.indexerManagementSvc.Update(ctx, GetActor(ctx), req.Id, ports.IndexerManagementInput{Name: req.Body.Name, Protocol: ports.IndexerProtocol(req.Body.Protocol), URL: req.Body.Url, Credential: req.Body.ApiKey, Categories: req.Body.Categories, ExpectedVersion: req.Body.ExpectedVersion}, req.Params.IdempotencyKey)
	if err != nil {
		return indexerFailure(ctx, err), nil
	}
	return managedIndexerResult{receipt: receipt}, nil
}

func (s *ServerHandler) DeleteIndexer(ctx context.Context, req DeleteIndexerRequestObject) (DeleteIndexerResponseObject, error) {
	if s.indexerManagementSvc == nil {
		return indexerFailure(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.indexerManagementSvc.Delete(ctx, GetActor(ctx), req.Id, req.Params.ExpectedVersion, req.Params.IdempotencyKey)
	if err != nil {
		return indexerFailure(ctx, err), nil
	}
	return managedIndexerResult{receipt: receipt}, nil
}

func (s *ServerHandler) GetIndexerCommandReceipt(ctx context.Context, req GetIndexerCommandReceiptRequestObject) (GetIndexerCommandReceiptResponseObject, error) {
	if s.indexerReceiptSvc == nil {
		return indexerFailure(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.indexerReceiptSvc.Read(ctx, GetActor(ctx), ports.IndexerMutationAction(req.Params.Action), req.Key)
	if err != nil {
		return indexerFailure(ctx, err), nil
	}
	return managedIndexerResult{receipt: receipt}, nil
}

func managedIndexerDetails(view ports.IndexerManagementView) IndexerDetails {
	// Public projections contain an origin only, including restored old receipts.
	display := ""
	if u, err := url.Parse(view.URL); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
		display = u.Scheme + "://" + u.Host
	}
	categories := slices.Clone(view.Categories)
	if categories == nil {
		categories = []int{}
	}
	failures := int(view.HealthFailures)
	reason := IndexerDetailsHealthReason(view.HealthReason)
	return IndexerDetails{Id: view.ID, Name: view.Name, Url: display, Protocol: IndexerProtocol(view.Protocol), LegacyV1: view.LegacyV1, Categories: categories, Health: view.Health, ConfigVersion: int(view.ConfigVersion), CreatedAt: &view.CreatedAt, HealthCheckedAt: view.HealthCheckedAt, HealthObservedAt: view.HealthObservedAt, HealthNextCheckAt: view.HealthNextCheckAt, HealthFailures: &failures, HealthReason: &reason}
}

func writeIndexerJSON(w http.ResponseWriter, status int, body any) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusNoContent {
		return nil
	}
	return json.NewEncoder(w).Encode(body)
}

type managedIndexerList struct{ items []IndexerDetails }

func (r managedIndexerList) VisitListIndexersResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, http.StatusOK, IndexerListResponse{Items: r.items})
}

type managedIndexerResult struct {
	receipt *ports.IndexerManagementReceipt
}

func (r managedIndexerResult) VisitCreateIndexerResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, http.StatusCreated, managedIndexerDetails(r.receipt.Indexer))
}
func (r managedIndexerResult) VisitUpdateIndexerResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, http.StatusOK, managedIndexerDetails(r.receipt.Indexer))
}
func (r managedIndexerResult) VisitDeleteIndexerResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, http.StatusNoContent, nil)
}
func (r managedIndexerResult) VisitGetIndexerCommandReceiptResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, http.StatusOK, IndexerCommandReceipt{Action: IndexerCommandReceiptAction(r.receipt.Action), Indexer: managedIndexerDetails(r.receipt.Indexer), Deleted: r.receipt.Deleted})
}

type managedIndexerFailure struct {
	status int
	body   ErrorResponse
}

func indexerFailure(ctx context.Context, err error) managedIndexerFailure {
	status, code, message := http.StatusServiceUnavailable, "INDEXER_UNAVAILABLE", "站点配置暂时不可用，请稍后重试。"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = http.StatusUnauthorized, "UNAUTHORIZED", "请重新登录。"
	case errors.Is(err, ports.ErrForbidden), errors.Is(err, ports.ErrUserDisabled):
		status, code, message = http.StatusForbidden, "FORBIDDEN", "需要当前有效的管理员会话。"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "未找到站点配置或命令回执。"
	case errors.Is(err, ports.ErrIndexerCommandConflict), errors.Is(err, ports.ErrCommandConflict):
		status, code, message = http.StatusConflict, "INDEXER_COMMAND_CONFLICT", "配置或原请求已变化，请重新读取。"
	case errors.Is(err, ports.ErrRejected):
		status, code, message = http.StatusBadRequest, "INVALID_INDEXER_COMMAND", "请检查站点协议、配置版本和输入。"
	}
	return managedIndexerFailure{status: status, body: ErrorResponse{Code: code, Message: message, RequestId: GetRequestID(ctx)}}
}
func (r managedIndexerFailure) VisitListIndexersResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, r.status, r.body)
}
func (r managedIndexerFailure) VisitCreateIndexerResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, r.status, r.body)
}
func (r managedIndexerFailure) VisitUpdateIndexerResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, r.status, r.body)
}
func (r managedIndexerFailure) VisitDeleteIndexerResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, r.status, r.body)
}
func (r managedIndexerFailure) VisitGetIndexerCommandReceiptResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, r.status, r.body)
}

func (s *ServerHandler) TestIndexer(ctx context.Context, req TestIndexerRequestObject) (TestIndexerResponseObject, error) {
	if GetActor(ctx) == nil {
		return indexerFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.searchSvc == nil {
		return indexerFailure(ctx, ports.ErrUnavailable), nil
	}
	alive, latency, message, err := s.searchSvc.TestIndexerForActor(ctx, GetActor(ctx), req.Id)
	if errors.Is(err, ports.ErrUnauthorized) || errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUserDisabled) || errors.Is(err, ports.ErrNotFound) {
		return indexerFailure(ctx, err), nil
	}
	if err != nil && message == "" {
		return indexerFailure(ctx, ports.ErrUnavailable), nil
	}
	ms := int(latency)
	return TestIndexer200JSONResponse{Alive: alive, LatencyMs: &ms, Message: message}, nil
}
func (r managedIndexerFailure) VisitTestIndexerResponse(w http.ResponseWriter) error {
	return writeIndexerJSON(w, r.status, r.body)
}
