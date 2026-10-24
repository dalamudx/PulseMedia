package config

import (
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/google/uuid"
	"io"
	"strings"
)

type DownloadRoutingRule struct {
	MediaType    string    `yaml:"media_type" json:"media_type"`
	IndexerID    uuid.UUID `yaml:"indexer_id" json:"indexer_id,omitempty"`
	DownloaderID uuid.UUID `yaml:"downloader_id" json:"downloader_id"`
}

type DownloadRoutingConfig struct {
	Enabled *bool                 `yaml:"enabled"`
	Rules   []DownloadRoutingRule `yaml:"rules"`
}

// IsEnabled preserves the legacy default and permits explicit single-client fallback.
func (c DownloadRoutingConfig) IsEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return len(c.Rules) > 0
}
func routingConfigError() error {
	return fmt.Errorf("%w: invalid download_routing.rules", ErrInvalidConfig)
}

func decodeDownloadRouteRules(raw string) ([]DownloadRoutingRule, error) {
	if len(raw) > 16384 {
		return nil, routingConfigError()
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var rules []DownloadRoutingRule
	if decoder.Decode(&rules) != nil || rules == nil {
		return nil, routingConfigError()
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, routingConfigError()
	}
	return rules, nil
}

func (c DownloadRoutingConfig) validate() error {
	if len(c.Rules) > 64 {
		return routingConfigError()
	}
	type scope struct {
		media string
		site  uuid.UUID
	}
	seen := make(map[scope]bool, len(c.Rules))
	for _, rule := range c.Rules {
		key := scope{rule.MediaType, rule.IndexerID}
		if rule.DownloaderID == uuid.Nil || (rule.MediaType != "" && rule.MediaType != "movie" && rule.MediaType != "tv") || seen[key] {
			return routingConfigError()
		}
		seen[key] = true
	}
	return nil
}

// RulesForSelection returns a fresh policy value; a worker must freeze its choice.
func (c DownloadRoutingConfig) RulesForSelection() []endpoint.DownloadRouteRule {
	result := make([]endpoint.DownloadRouteRule, 0, len(c.Rules))
	for _, rule := range c.Rules {
		result = append(result, endpoint.DownloadRouteRule{MediaType: rule.MediaType, IndexerID: rule.IndexerID, DownloaderID: rule.DownloaderID})
	}
	return result
}
