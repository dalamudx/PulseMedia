package acquisition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strconv"
	"strings"
)

type MovieSelectionService struct {
	repo     ports.MovieSelectionRepository
	provider ports.MetadataProvider
	clock    ports.Clock
}

func NewMovieSelectionService(repo ports.MovieSelectionRepository, provider ports.MetadataProvider, clock ports.Clock) *MovieSelectionService {
	return &MovieSelectionService{repo, provider, clock}
}
func validMovieSelection(c ports.MovieSelection) bool {
	return c.TMDBID > 0 && c.RootID != [16]byte{} && c.CandidateID != [16]byte{} && c.DownloaderID != [16]byte{} && strings.TrimSpace(c.PolicyID) == c.PolicyID && c.PolicyID != "" && len(c.PolicyID) <= 100
}
func movieSelectionStamp(snapshot ports.MovieSelectionSnapshot, c ports.MovieSelection, m ports.SelectedMovieMetadata) string {
	raw, _ := json.Marshal([]any{"movie.selection.v1", snapshot.Stamp, c, m, media.EpisodeParserVersion, media.CandidateDecisionVersion})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *MovieSelectionService) preview(ctx context.Context, a *ports.Actor, c ports.MovieSelection) (*ports.MovieSelectionPreview, *ports.SelectedMovieMetadata, error) {
	if a == nil {
		return nil, nil, ports.ErrUnauthorized
	}
	if !validMovieSelection(c) {
		return nil, nil, ports.ErrInvalidMovieSelection
	}
	before, e := s.repo.ReadMovieSelection(ctx, a, c)
	if e != nil {
		return nil, nil, e
	}
	if s.provider == nil {
		return nil, nil, ports.ErrUnavailable
	}
	metadata, e := s.provider.GetMovieDetails(ctx, c.TMDBID, "zh-CN")
	if e != nil || metadata == nil {
		return nil, nil, ports.ErrUnavailable
	}
	if metadata.TMDBID != c.TMDBID || metadata.MediaType != "movie" || metadata.Year < 1888 || metadata.Year > s.clock.Now().Year() || strings.TrimSpace(metadata.Title) == "" || media.UnsafeCandidateTitle(metadata.Title) || media.UnsafeCandidateTitle(metadata.OriginalTitle) {
		return nil, nil, ports.ErrInvalidMovieSelection
	}
	after, e := s.repo.ReadMovieSelection(ctx, a, c)
	if e != nil {
		return nil, nil, e
	}
	if before.Stamp != after.Stamp {
		return nil, nil, ports.ErrMovieSelectionConflict
	}
	m := &ports.SelectedMovieMetadata{TMDBID: metadata.TMDBID, Title: metadata.Title, OriginalTitle: metadata.OriginalTitle, Year: metadata.Year, IMDbID: metadata.IMDbID}
	rule := after.Rule
	rule.TargetTitle = m.Title
	rule.Year = m.Year
	rule.Identities = map[string]string{"tmdb.movie": strconv.FormatInt(c.TMDBID, 10)}
	if m.IMDbID != "" {
		rule.Identities["imdb"] = m.IMDbID
	}
	decision := media.ExplainMovieCandidate(rule)
	matched := m.Title
	if decision.Decision != "accept" && m.OriginalTitle != "" {
		rule.TargetTitle = m.OriginalTitle
		alternative := media.ExplainMovieCandidate(rule)
		if alternative.Decision == "accept" {
			decision = alternative
			matched = m.OriginalTitle
		}
	}
	if !after.Current {
		decision = media.CandidateRuleDecision{Decision: "hold", Reason: after.Reason}
	}
	return &ports.MovieSelectionPreview{Selection: c, InputStamp: movieSelectionStamp(after, c, *m), Title: m.Title, Year: m.Year, Decision: decision.Decision, Reason: decision.Reason, Score: decision.Score, ObservedAt: after.ObservedAt, MatchedTitle: matched}, m, nil
}
func (s *MovieSelectionService) Preview(ctx context.Context, a *ports.Actor, c ports.MovieSelection) (*ports.MovieSelectionPreview, error) {
	p, _, e := s.preview(ctx, a, c)
	return p, e
}
func (s *MovieSelectionService) Commit(ctx context.Context, a *ports.Actor, c ports.MovieSelectionCommand, key string) (*ports.MovieSelectionReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !c.Confirmed || !validMovieSelection(c.Selection) || len(c.ExpectedInputStamp) != 64 || key == "" || len(key) > 160 {
		return nil, ports.ErrInvalidMovieSelection
	}
	raw, _ := json.Marshal([]any{"movie.selection.commit.v1", c})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	r, e := s.repo.ReadMovieSelectionReceipt(ctx, a, c.Selection, key, hash)
	if e != nil || r != nil {
		return r, e
	}
	recoverReceipt := func(cause error) (*ports.MovieSelectionReceipt, error) {
		r, x := s.repo.ReadMovieSelectionReceipt(ctx, a, c.Selection, key, hash)
		if x != nil || r != nil {
			return r, x
		}
		return nil, cause
	}
	p, m, e := s.preview(ctx, a, c.Selection)
	if e != nil {
		return recoverReceipt(e)
	}
	if p.InputStamp != c.ExpectedInputStamp {
		return recoverReceipt(ports.ErrMovieSelectionConflict)
	}
	if p.Decision != "accept" {
		return recoverReceipt(ports.ErrAcquisitionUnsupportedCoverage)
	}
	return s.repo.CommitMovieSelection(ctx, a, c.Selection, *m, c.ExpectedInputStamp, key, hash)
}
