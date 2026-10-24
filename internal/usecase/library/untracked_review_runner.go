package library

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const untrackedReviewParserVersion = "untracked-review.v1/" + media.EpisodeParserVersion

func untrackedReviewSelection(in ports.UntrackedReviewInput) string {
	if in.TMDBID > 0 {
		return "explicit"
	}
	return "automatic"
}
func untrackedReviewReason(reason string) bool {
	switch reason {
	case "ready", "coverage_unresolved", "media_type_conflict", "parse_untrusted", "unknown_year", "identity_incomplete", "identity_unresolved", "identity_ambiguous", "detail_mismatch", "season_unresolved", "season_incomplete", "episode_metadata_invalid", "episode_unresolved":
		return true
	}
	return false
}

// UntrackedReviewRunner produces identity evidence only; it has no linking port.
type UntrackedReviewRunner struct {
	repo     ports.UntrackedReviewExecutionRepository
	files    ports.RootFilesystem
	metadata ports.WatchlistIdentityMetadata
	episodes ports.TVEpisodeProvider
}

func NewUntrackedReviewRunner(repo ports.UntrackedReviewExecutionRepository, files ports.RootFilesystem, metadata ports.WatchlistIdentityMetadata, episodes ports.TVEpisodeProvider) *UntrackedReviewRunner {
	return &UntrackedReviewRunner{repo: repo, files: files, metadata: metadata, episodes: episodes}
}
func untrackedReviewSnapshotValid(s ports.UntrackedReviewSnapshot, lease ports.JobLease) bool {
	return lease.ID != uuid.Nil && untrackedScopeKey(lease.Owner) && lease.Token > 0 && s.Lease == lease && s.Request.JobID == lease.ID && untrackedReviewRequestValid(s.Request, s.Scope.OwnerID.String(), s.Scope.ID, s.File.ID) && s.Scope.Enabled && untrackedReviewScopeValid(s.Scope, s.Request.ScopeID) && s.Scope.TargetMediaType == s.Request.Input.MediaType && s.Scope.Version == s.Request.Input.ScopeVersion && untrackedReviewFileValid(s.File, s.Scope, s.Request.FileID) && s.File.Revision == s.Request.Input.FileRevision
}
func untrackedReviewSnapshotEqual(a, b ports.UntrackedReviewSnapshot) bool {
	return a.Lease == b.Lease && untrackedReviewMaterialEqual(
		ports.UntrackedConfirmationMaterial{Scope: a.Scope, File: a.File, Review: ports.UntrackedReview{Request: a.Request}},
		ports.UntrackedConfirmationMaterial{Scope: b.Scope, File: b.File, Review: ports.UntrackedReview{Request: b.Request}},
	)
}

func (r *UntrackedReviewRunner) Run(ctx context.Context, lease ports.JobLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.repo == nil || r.files == nil {
		return ports.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	capture, err := r.repo.LoadUntrackedReview(ctx, lease)
	if err != nil || ctx.Err() != nil {
		return untrackedReviewError(ctx, err)
	}
	if capture == nil {
		return ports.ErrUntrackedReviewStale
	}
	original := capture.UntrackedReviewSnapshot()
	if !untrackedReviewSnapshotValid(original, lease) {
		return ports.ErrUntrackedReviewStale
	}
	validate := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !untrackedReviewSnapshotEqual(original, capture.UntrackedReviewSnapshot()) {
			return ports.ErrUntrackedReviewStale
		}
		err := r.repo.ValidateUntrackedReview(ctx, capture)
		if err != nil || ctx.Err() != nil {
			return untrackedReviewError(ctx, err)
		}
		if !untrackedReviewSnapshotEqual(original, capture.UntrackedReviewSnapshot()) {
			return ports.ErrUntrackedReviewStale
		}
		return nil
	}
	if err := validate(); err != nil {
		return err
	}
	physical, err := untrackedReviewOpen(ctx, r.files, original.Scope, original.File)
	if err != nil {
		return err
	}
	defer physical.close()
	if err := validate(); err != nil {
		return err
	}
	if err := physical.check(ctx); err != nil {
		return err
	}
	evidence, err := r.identify(ctx, original)
	if err != nil || ctx.Err() != nil {
		return untrackedReviewError(ctx, err)
	}
	if !untrackedReviewEvidenceValid(&evidence, original.Request.Input) {
		return ports.ErrUnavailable
	}
	if err := validate(); err != nil {
		return err
	}
	if err := physical.check(ctx); err != nil {
		return err
	}
	if !untrackedReviewSnapshotEqual(original, capture.UntrackedReviewSnapshot()) {
		return ports.ErrUntrackedReviewStale
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err = r.repo.CompleteUntrackedReview(ctx, capture, evidence)
	if err != nil || ctx.Err() != nil {
		return untrackedReviewError(ctx, err)
	}
	return nil
}

func untrackedReviewCoverage(p media.EpisodeRecognition, kind string) bool {
	if p.ParserVersion != media.EpisodeParserVersion || p.RequiresReview || p.IsAnime || p.AbsoluteEpisode != 0 || string(p.MediaType) != kind {
		return false
	}
	if kind == "movie" {
		return p.Season == 0 && len(p.Episodes) == 0 && len(p.UnitKeys) == 1 && p.UnitKeys[0] == "movie"
	}
	if p.Season < 1 || p.Season > 99 || len(p.Episodes) == 0 || len(p.Episodes) > 128 || len(p.UnitKeys) != len(p.Episodes) {
		return false
	}
	switch p.Reason {
	case "standard_season_episode", "alt_season_episode", "multi_episode_range", "alt_multi_episode_range", "explicit_episode_list":
	default:
		return false
	}
	seen := map[int]bool{}
	for i, ep := range p.Episodes {
		if ep < 1 || ep > 9999 || seen[ep] || p.UnitKeys[i] != media.FormatEpisodeUnitKey(p.Season, ep) {
			return false
		}
		seen[ep] = true
	}
	return true
}
func untrackedReviewTitleMatches(query string, title, original string) bool {
	key := media.WatchlistTitleKey(query)
	return key != "" && (key == media.WatchlistTitleKey(title) || key == media.WatchlistTitleKey(original))
}
func untrackedReviewMetadataValid(m *ports.MovieMetadata, kind string) bool {
	return m != nil && m.TMDBID > 0 && m.MediaType == kind && media.WatchlistTitleKey(m.Title) != "" && (m.OriginalTitle == "" || media.WatchlistTitleKey(m.OriginalTitle) != "") && m.Year >= 1800 && m.Year <= 9999
}

func (r *UntrackedReviewRunner) identify(ctx context.Context, s ports.UntrackedReviewSnapshot) (ports.UntrackedReviewEvidence, error) {
	in := s.Request.Input
	// Both parsers see the original relative path, including parent coverage conflicts.
	movie := media.Recognize(s.File.RelativePath)
	parsed := media.RecognizeRelease(s.File.RelativePath)
	e := ports.UntrackedReviewEvidence{ParserVersion: untrackedReviewParserVersion, ParsedTitle: parsed.Title, ParsedYear: parsed.Year, Selection: untrackedReviewSelection(in), Reason: "coverage_unresolved"}
	if in.MediaType == "movie" {
		e.ParsedTitle, e.ParsedYear = movie.Title, movie.Year
	}
	if media.WatchlistTitleKey(e.ParsedTitle) == "" {
		e.ParsedTitle = ""
	}
	if e.ParsedYear < 1800 || e.ParsedYear > 9999 {
		e.ParsedYear = 0
	}
	if in.TMDBID == 0 && parsed.Reason == "title_only_without_year" {
		e.Reason = "unknown_year"
		return e, nil
	}
	if string(parsed.MediaType) != in.MediaType {
		e.Reason = "media_type_conflict"
		return e, nil
	}
	if !untrackedReviewCoverage(parsed, in.MediaType) {
		return e, nil
	}
	if in.TMDBID == 0 {
		if e.ParsedYear < 1800 || e.ParsedYear > 9999 {
			e.ParsedYear = 0
			e.Reason = "unknown_year"
			return e, nil
		}
		if media.WatchlistTitleKey(e.ParsedTitle) == "" || (in.MediaType == "movie" && movie.RequiresReview) {
			e.Reason = "parse_untrusted"
			return e, nil
		}
	}
	if r.metadata == nil {
		return e, ports.ErrUnavailable
	}
	id := in.TMDBID
	var selected *ports.MovieMetadata
	if id == 0 {
		result, err := r.metadata.SearchMediaIdentityCandidates(ctx, e.ParsedTitle, in.MediaType, e.ParsedYear)
		if ctx.Err() != nil {
			return e, ctx.Err()
		}
		if err != nil {
			return e, ports.ErrUnavailable
		}
		if !result.Complete || len(result.Candidates) > 5000 {
			e.Reason = "identity_incomplete"
			return e, nil
		}
		matches := map[int64]*ports.MovieMetadata{}
		for _, candidate := range result.Candidates {
			if !untrackedReviewMetadataValid(candidate, in.MediaType) {
				e.Reason = "identity_incomplete"
				return e, nil
			}
			if candidate.Year != e.ParsedYear || !untrackedReviewTitleMatches(e.ParsedTitle, candidate.Title, candidate.OriginalTitle) {
				continue
			}
			if old := matches[candidate.TMDBID]; old != nil && (media.WatchlistTitleKey(old.Title) != media.WatchlistTitleKey(candidate.Title) || media.WatchlistTitleKey(old.OriginalTitle) != media.WatchlistTitleKey(candidate.OriginalTitle) || old.Year != candidate.Year) {
				e.Reason = "identity_incomplete"
				return e, nil
			}
			copy := *candidate
			matches[candidate.TMDBID] = &copy
		}
		if len(matches) == 0 {
			e.Reason = "identity_unresolved"
			return e, nil
		}
		if len(matches) != 1 {
			e.Reason = "identity_ambiguous"
			return e, nil
		}
		for candidateID, candidate := range matches {
			id, selected = candidateID, candidate
		}
	}
	var details *ports.MovieMetadata
	var tv *ports.TVDetails
	if err := ctx.Err(); err != nil {
		return e, err
	}
	if in.MediaType == "movie" {
		var err error
		details, err = r.metadata.GetMovieDetails(ctx, id, "zh-CN")
		if ctx.Err() != nil {
			return e, ctx.Err()
		}
		if err != nil {
			return e, ports.ErrUnavailable
		}
	} else {
		var err error
		tv, err = r.metadata.GetTVDetails(ctx, id, "zh-CN")
		if ctx.Err() != nil {
			return e, ctx.Err()
		}
		if err != nil {
			return e, ports.ErrUnavailable
		}
		if tv != nil {
			details = &ports.MovieMetadata{TMDBID: tv.TMDBID, MediaType: "tv", Title: tv.Title, OriginalTitle: tv.OriginalTitle, Year: tv.Year}
		}
	}
	if !untrackedReviewMetadataValid(details, in.MediaType) || details.TMDBID != id {
		e.Reason = "detail_mismatch"
		return e, nil
	}
	if selected != nil && (details.Year != selected.Year || media.WatchlistTitleKey(details.Title) != media.WatchlistTitleKey(selected.Title) || media.WatchlistTitleKey(details.OriginalTitle) != media.WatchlistTitleKey(selected.OriginalTitle) || !untrackedReviewTitleMatches(e.ParsedTitle, details.Title, details.OriginalTitle)) {
		e.Reason = "detail_mismatch"
		return e, nil
	}
	candidate := &ports.UntrackedReviewCandidate{Key: "tmdb." + in.MediaType + ":" + strconv.FormatInt(id, 10), Namespace: "tmdb." + in.MediaType, TMDBID: id, Title: details.Title, OriginalTitle: details.OriginalTitle, Year: details.Year}
	if in.MediaType == "movie" {
		candidate.Units = []ports.UntrackedReviewUnit{{UnitKey: "movie"}}
	} else {
		units, reason, err := r.tvUnits(ctx, id, tv, parsed)
		if err != nil {
			return e, err
		}
		if reason != "" {
			e.Reason = reason
			return e, nil
		}
		candidate.Units = units
	}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	if !untrackedReviewCandidateValid(candidate, in.MediaType) {
		return e, ports.ErrUnavailable
	}
	e.Complete, e.Reason, e.Candidate = true, "ready", candidate
	return e, nil
}

func (r *UntrackedReviewRunner) tvUnits(ctx context.Context, id int64, details *ports.TVDetails, parsed media.EpisodeRecognition) ([]ports.UntrackedReviewUnit, string, error) {
	if details == nil || len(details.Seasons) == 0 || len(details.Seasons) > 5000 || details.SeasonsCount < 1 || details.SeasonsCount > 5000 || details.EpisodesCount < 1 {
		return nil, "season_unresolved", nil
	}
	seen := map[int]bool{}
	declared, regular, total := 0, 0, 0
	for _, season := range details.Seasons {
		if season.SeasonNumber < 0 || season.SeasonNumber > 99 || seen[season.SeasonNumber] || season.EpisodeCount < 0 || season.EpisodeCount > 5000 {
			return nil, "season_unresolved", nil
		}
		seen[season.SeasonNumber] = true
		if season.SeasonNumber > 0 {
			regular++
			total += season.EpisodeCount
		}
		if season.SeasonNumber == parsed.Season {
			declared = season.EpisodeCount
		}
	}
	if regular != details.SeasonsCount || total != details.EpisodesCount || declared < 1 {
		return nil, "season_unresolved", nil
	}
	if r.episodes == nil {
		return nil, "", ports.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	season, err := r.episodes.GetTVSeasonMetadata(ctx, id, parsed.Season, "zh-CN")
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if err != nil {
		return nil, "", ports.ErrUnavailable
	}
	if season == nil || !season.Complete || season.SeasonNumber != parsed.Season || len(season.Episodes) != declared || len(season.Episodes) > 5000 {
		return nil, "season_incomplete", nil
	}
	numbers, ids := map[int]ports.TVEpisodeMetadata{}, map[int64]bool{}
	for _, episode := range season.Episodes {
		if episode.EpisodeNumber < 1 || episode.EpisodeNumber > 9999 || episode.TMDBEpisodeID < 1 || ids[episode.TMDBEpisodeID] || !untrackedReviewDate(episode.AirDate) || episode.AbsoluteNumber != nil {
			return nil, "episode_metadata_invalid", nil
		}
		if _, exists := numbers[episode.EpisodeNumber]; exists {
			return nil, "episode_metadata_invalid", nil
		}
		numbers[episode.EpisodeNumber], ids[episode.TMDBEpisodeID] = episode, true
	}
	units := make([]ports.UntrackedReviewUnit, 0, len(parsed.Episodes))
	for _, number := range parsed.Episodes {
		ep, ok := numbers[number]
		if !ok {
			return nil, "episode_unresolved", nil
		}
		var date *time.Time
		if ep.AirDate != nil {
			copy := *ep.AirDate
			date = &copy
		}
		units = append(units, ports.UntrackedReviewUnit{UnitKey: media.FormatEpisodeUnitKey(parsed.Season, number), TMDBEpisodeID: ep.TMDBEpisodeID, Season: parsed.Season, Episode: number, AirDate: date})
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Episode < units[j].Episode })
	return units, "", nil
}
