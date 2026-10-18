package search

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Shared read executions keep their physical permit independent of a subscriber
// lease. Only the persisted task scope may attach that evidence to a round.
func (s *Service) ExecuteEndpointJob(ctx context.Context, task ports.SearchEndpointTask) error {
	if s.endpointStore == nil {
		return ports.ErrUnavailable
	}
	scope, e := s.endpointStore.Load(ctx, task)
	if e != nil {
		return e
	}
	var queryID uuid.UUID
	if scope.MaySearch {
		var chain *pageChain
		if scope.NextPage > 0 && scope.PageTotal != nil {
			chain = &pageChain{total: *scope.PageTotal, seen: make(map[string]bool)}
			for _, guid := range scope.SeenGUIDs {
				chain.seen[guid] = true
			}
		}
		callCtx, cancel := context.WithDeadline(ctx, scope.Deadline)
		keyword := scope.Title
		if scope.Year > 0 {
			keyword = fmt.Sprintf("%s %d", keyword, scope.Year)
		}
		result, e := s.execute(callCtx, task.IndexerID, keyword, scope.NextPage, uuid.Nil, scope.ConfigVersion, nil, false, chain)
		interrupted := callCtx.Err() != nil
		cancel()
		if e == nil {
			if scope.NextPage > 0 && result.State != "succeeded" && !interrupted {
				return ports.ErrUnavailable
			}
			queryID = result.QueryID
		} else if errors.Is(e, ports.ErrUnavailable) && !interrupted {
			return e
		}
	}
	return s.endpointStore.Complete(ctx, scope, queryID)
}
