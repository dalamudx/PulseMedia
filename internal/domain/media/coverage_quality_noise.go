package media

import "regexp"

// Only an explicitly named audio format followed by complete standard channel
// multiplicities is removed from coverage scanning. This does not generate
// measured audio facts and leaves the original name/quality input unchanged.
var audioChannelCountRegex = regexp.MustCompile(`(?i)\b(?:ac3|eac3|aac|dts|truehd|flac)[ ._]+(?:1\.0|2\.0|5\.1|6\.1|7\.1|7\.2)x[1-9][0-9]*(?:\+(?:1\.0|2\.0|5\.1|6\.1|7\.1|7\.2)x[1-9][0-9]*)*(?:$|[ ),\]._\-])`)

func maskExplicitAudioCounts(s string) string {
	return audioChannelCountRegex.ReplaceAllString(s, " ")
}
