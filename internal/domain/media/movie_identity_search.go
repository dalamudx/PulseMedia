package media

// MovieSearchIdentity contains only the identity evidence returned by search.
type MovieSearchIdentity struct {
	TMDBID                          int64
	Title, OriginalTitle, MediaType string
	Year                            int
}

// SelectMovieSearchIdentity requires one exact title/year match in a complete set.
// It establishes identity only; quality, file authority, and execution are separate.
func SelectMovieSearchIdentity(rec Recognition, candidates []MovieSearchIdentity, complete bool) (int, bool) {
	if !complete || rec.Identity != nil || rec.RequiresReview || rec.Confidence < 0.7 || rec.Year < 1 {
		return -1, false
	}
	title := normalizeMovieTitle(rec.Title)
	if title == "" {
		return -1, false
	}
	selected := -1
	for i, c := range candidates {
		if c.TMDBID < 1 || (c.MediaType != "" && Type(c.MediaType) != TypeMovie) {
			return -1, false
		}
		matches := normalizeMovieTitle(c.Title) == title || (c.OriginalTitle != "" && normalizeMovieTitle(c.OriginalTitle) == title)
		if !matches {
			continue
		}
		if c.Year < 1 {
			return -1, false
		}
		if c.Year != rec.Year {
			continue
		}
		if selected >= 0 {
			return -1, false
		}
		selected = i
	}
	return selected, selected >= 0
}
