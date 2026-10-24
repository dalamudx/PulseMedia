package quality

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrInvalidRules = errors.New("invalid quality rules")

func KnownProfile(p Profile) bool {
	resolution := p.Resolution == Res2160p || p.Resolution == Res1080p || p.Resolution == Res720p || p.Resolution == Res480p
	source := p.Source == SourceRemux || p.Source == SourceBluray || p.Source == SourceWEBDL || p.Source == SourceWEBRip || p.Source == SourceHDTV
	return resolution && source && p.SourceParserVersion == SourceTokenParserVersion
}

func ValidateRules(r PolicyRules) error {
	if r.CutoffScore < 0 || r.CutoffScore > 200015 || len(r.ResolutionOrder) < 1 || len(r.ResolutionOrder) > 4 || len(r.SourceOrder) < 1 || len(r.SourceOrder) > 5 {
		return ErrInvalidRules
	}
	for k, v := range r.ResolutionOrder {
		if (k != Res2160p && k != Res1080p && k != Res720p && k != Res480p) || v < 0 || v > 100000 {
			return ErrInvalidRules
		}
	}
	for k, v := range r.SourceOrder {
		if (k != SourceRemux && k != SourceBluray && k != SourceWEBDL && k != SourceWEBRip && k != SourceHDTV) || v < 0 || v > 100000 {
			return ErrInvalidRules
		}
	}
	if ValidateCustomWords(r.CustomWords) != nil {
		return ErrInvalidRules
	}
	for _, items := range [][]string{r.RejectTerms, r.RejectGroups} {
		if len(items) > 100 {
			return ErrInvalidRules
		}
		seen := map[string]bool{}
		for _, term := range items {
			key := strings.ToLower(strings.TrimSpace(term))
			if key == "" || utf8.RuneCountInString(term) > 128 || strings.ContainsAny(term, "\r\n\x00") || seen[key] {
				return ErrInvalidRules
			}
			seen[key] = true
		}
	}
	return nil
}
