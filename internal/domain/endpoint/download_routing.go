package endpoint

import (
	"errors"
	"github.com/google/uuid"
)

const DownloadRoutePolicyVersion int64 = 1

var (
	ErrInvalidDownloadRoute   = errors.New("invalid download routing inputs")
	ErrNoDownloadRouteClient  = errors.New("no eligible download client")
	ErrAmbiguousDownloadRoute = errors.New("download routing requires an unambiguous rule")
)

// Eligibility is supplied by the authorization boundary, never inferred from a name.
type DownloadRouteClient struct {
	ID       uuid.UUID
	Eligible bool
}

// Empty media type and nil indexer match all values in that dimension.
type DownloadRouteRule struct {
	MediaType    string
	IndexerID    uuid.UUID
	DownloaderID uuid.UUID
}

type DownloadRouteRequest struct {
	MediaType            string
	IndexerID            uuid.UUID
	ExplicitDownloaderID *uuid.UUID
}

type DownloadRouteDecision struct {
	DownloaderID  uuid.UUID
	PolicyVersion int64
	Basis         string
}

// SelectDownloadRoute resolves a new choice only. Recovery uses its frozen result.
func SelectDownloadRoute(request DownloadRouteRequest, clients []DownloadRouteClient, rules []DownloadRouteRule) (DownloadRouteDecision, error) {
	empty := DownloadRouteDecision{}
	if request.MediaType != "movie" && request.MediaType != "tv" {
		return empty, ErrInvalidDownloadRoute
	}
	eligible := make(map[uuid.UUID]bool, len(clients))
	for _, client := range clients {
		if client.ID == uuid.Nil {
			return empty, ErrInvalidDownloadRoute
		}
		if _, exists := eligible[client.ID]; exists {
			return empty, ErrInvalidDownloadRoute
		}
		eligible[client.ID] = client.Eligible
	}
	decision := func(id uuid.UUID, basis string) (DownloadRouteDecision, error) {
		if !eligible[id] {
			return empty, ErrNoDownloadRouteClient
		}
		return DownloadRouteDecision{DownloaderID: id, PolicyVersion: DownloadRoutePolicyVersion, Basis: basis}, nil
	}
	// Existing manual intent is independent of unrelated automatic routing settings.
	if request.ExplicitDownloaderID != nil {
		if *request.ExplicitDownloaderID == uuid.Nil {
			return empty, ErrInvalidDownloadRoute
		}
		return decision(*request.ExplicitDownloaderID, "explicit")
	}
	best, selected, ambiguous := -1, uuid.Nil, false
	for _, rule := range rules {
		if rule.DownloaderID == uuid.Nil || (rule.MediaType != "" && rule.MediaType != "movie" && rule.MediaType != "tv") {
			return empty, ErrInvalidDownloadRoute
		}
		if rule.MediaType != "" && rule.MediaType != request.MediaType {
			continue
		}
		if rule.IndexerID != uuid.Nil && rule.IndexerID != request.IndexerID {
			continue
		}
		specificity := 0
		if rule.MediaType != "" {
			specificity++
		}
		if rule.IndexerID != uuid.Nil {
			specificity += 2
		}
		if specificity > best {
			best, selected, ambiguous = specificity, rule.DownloaderID, false
		} else if specificity == best && selected != rule.DownloaderID {
			ambiguous = true
		}
	}
	if ambiguous {
		return empty, ErrAmbiguousDownloadRoute
	}
	if best >= 0 {
		return decision(selected, []string{"default_rule", "media_rule", "site_rule", "site_media_rule"}[best])
	}
	selected = uuid.Nil
	for id, available := range eligible {
		if !available {
			continue
		}
		if selected != uuid.Nil {
			return empty, ErrAmbiguousDownloadRoute
		}
		selected = id
	}
	return decision(selected, "single_client")
}
