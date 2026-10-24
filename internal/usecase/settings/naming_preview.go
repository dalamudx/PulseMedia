package settings

import (
	"context"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/command"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type NamingPreviewRequest struct {
	MediaType string
	Input     naming.MediaTemplateInput
	Rules     *naming.TemplateRules
}

type NamingPreview struct {
	RelativePath string
	Version      int64
	Draft        bool
}

// PreviewNaming formats a sample. It does not inspect files or authorize a plan.
func (s *Service) PreviewNaming(ctx context.Context, actor *ports.Actor, request NamingPreviewRequest) (*NamingPreview, error) {
	if err := command.AuthorizeSession(actor, ports.RoleAdmin); err != nil {
		return nil, err
	}
	if request.MediaType != "movie" && request.MediaType != "tv" {
		return nil, ports.ErrInvalidSettingCommand
	}
	input := request.Input
	if strings.TrimSpace(input.Title) == "" || len(input.Title) > 1024 || len(input.Extension) > 16 || input.Year < 0 || input.Year > 9999 || input.TMDBID < 0 || len(input.IMDBID) > 32 {
		return nil, ports.ErrInvalidSettingCommand
	}
	// This read rechecks the current database account and holds its shared lock
	// through the configuration snapshot; a stale actor cannot preview settings.
	rows, err := s.repo.ListSettings(ctx, actor)
	if err != nil {
		return nil, err
	}
	rules := naming.TemplateRules{}
	var version int64
	for _, row := range rows {
		if row.Key != naming.TemplateSettingKey {
			continue
		}
		version = row.Version
		if request.Rules == nil {
			rules, err = naming.DecodeTemplateRules(row.Value)
			if err != nil || version < 1 {
				return nil, ports.ErrUnavailable
			}
		}
	}
	if request.Rules != nil {
		rules = *request.Rules
		if naming.ValidateTemplateRules(rules) != nil {
			return nil, ports.ErrInvalidSettingCommand
		}
	}
	var target string
	if request.MediaType == "tv" {
		target, err = rules.EpisodeRelative(input)
	} else {
		target, err = rules.MovieRelative(input)
	}
	if err != nil {
		return nil, ports.ErrInvalidSettingCommand
	}
	// Defaults keep their historical formatting; all previews still require a
	// safe video path so malformed sample metadata never produces a usable path.
	if eligible, _ := naming.IsPrimaryMediaPath(target); !eligible || strings.ContainsAny(input.Title, "\x00\r\n\\/") {
		return nil, ports.ErrInvalidSettingCommand
	}
	return &NamingPreview{RelativePath: target, Version: version, Draft: request.Rules != nil}, nil
}
