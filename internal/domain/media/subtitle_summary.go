package media

import "strings"

// SubtitleSummary describes cue coverage, not the duration of the video.
type SubtitleSummary struct {
	CueCount int
	StartMS  int64
	EndMS    int64
}

func SummarizeChineseSRT(input []byte) (SubtitleSummary, error) {
	normalized, err := NormalizeChineseSRT(input)
	if err != nil {
		return SubtitleSummary{}, err
	}
	defer clear(normalized)
	var out SubtitleSummary
	for _, cue := range strings.Split(strings.TrimSpace(string(normalized)), "\n\n") {
		lines := strings.SplitN(cue, "\n", 3)
		start, _ := srtMilliseconds(lines[1][:12])
		end, _ := srtMilliseconds(lines[1][17:])
		if out.CueCount == 0 || start < out.StartMS {
			out.StartMS = start
		}
		if end > out.EndMS {
			out.EndMS = end
		}
		out.CueCount++
	}
	return out, nil
}
