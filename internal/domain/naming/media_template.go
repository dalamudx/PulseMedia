package naming

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// MediaTemplateInput contains metadata for one movie or one identified episode.
// It is formatting input, not evidence of file ownership or execution authority.
type MediaTemplateInput struct {
	Title     string
	Year      int
	Season    int
	Episode   int
	Extension string
	Quality   quality.Profile
	TMDBID    int64
	IMDBID    string
}

func (rules TemplateRules) MovieRelative(input MediaTemplateInput) (string, error) {
	if rules.MovieTemplate == "" {
		return CleanRelPath(filepath.Join(FormatMovieDir(input.Title, input.Year), FormatMovieFile(input.Title, input.Year, input.Quality, input.Extension)))
	}
	return renderMediaTemplate(rules.MovieTemplate, input, false)
}

func (rules TemplateRules) EpisodeRelative(input MediaTemplateInput) (string, error) {
	if rules.EpisodeTemplate == "" {
		return FormatEpisodeRel(input.Title, input.Year, input.Season, input.Episode, input.Quality, input.Extension)
	}
	return renderMediaTemplate(rules.EpisodeTemplate, input, true)
}

func renderMediaTemplate(template string, input MediaTemplateInput, episode bool) (string, error) {
	extension := strings.ToLower(input.Extension)
	if !strings.HasPrefix(extension, ".") {
		extension = "." + extension
	}
	if !videoExtensions[extension] || strings.TrimSpace(input.Title) == "" {
		return "", ErrInvalidNamingTemplate
	}
	values := map[string]string{"title": sanitizeFileName(input.Title), "extension": strings.TrimPrefix(extension, ".")}
	if input.Year > 0 && input.Year <= 9999 {
		values["year"] = strconv.Itoa(input.Year)
	}
	if input.TMDBID > 0 {
		values["tmdb_id"] = strconv.FormatInt(input.TMDBID, 10)
	}
	if input.IMDBID != "" {
		values["imdb_id"] = input.IMDBID
	}
	if episode {
		if input.Season < 0 || input.Season > 999 || input.Episode <= 0 || input.Episode > 9999 {
			return "", ErrInvalidNamingTemplate
		}
		values["season"] = fmt.Sprintf("%02d", input.Season)
		values["episode"] = fmt.Sprintf("%02d", input.Episode)
		values["episode_key"] = fmt.Sprintf("S%02dE%02d", input.Season, input.Episode)
	}
	profileTemplateValues(values, input.Quality)
	target, err := FormatNamingTemplate(template, values)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(target), extension) {
		return "", ErrInvalidNamingTemplate
	}
	if eligible, _ := IsPrimaryMediaPath(target); !eligible {
		return "", ErrInvalidNamingTemplate
	}
	return target, nil
}

func profileTemplateValues(values map[string]string, profile quality.Profile) {
	if profile.ResolutionParserVersion == quality.ResolutionTokenParserVersion {
		switch profile.Resolution {
		case quality.Res2160p, quality.Res1080p, quality.Res720p, quality.Res480p:
			values["resolution"] = string(profile.Resolution)
		}
	}
	if profile.SourceParserVersion == quality.SourceTokenParserVersion {
		switch profile.Source {
		case quality.SourceRemux, quality.SourceBluray, quality.SourceWEBDL, quality.SourceWEBRip, quality.SourceHDTV:
			values["source"] = string(profile.Source)
		}
	}
	if profile.CodecParserVersion == quality.CodecTokenParserVersion {
		switch profile.Codec {
		case quality.CodecHEVC, quality.CodecH264, quality.CodecAV1:
			values["codec"] = string(profile.Codec)
		}
	}
	if quality.VerifiedHDR(profile) {
		values["hdr"] = string(profile.HDR)
	}
	for key, value := range map[string]string{"audio": profile.Audio, "edition": profile.Edition, "group": profile.Group} {
		if value != "" {
			values[key] = value
		}
	}
}
