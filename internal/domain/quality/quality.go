package quality

import (
	"strings"
	"unicode"
)

type Resolution string

const (
	Res2160p Resolution = "2160p"
	Res1080p Resolution = "1080p"
	Res720p  Resolution = "720p"
	Res480p  Resolution = "480p"
)

type Source string

const (
	SourceRemux  Source = "Remux"
	SourceBluray Source = "Bluray"
	SourceWEBDL  Source = "WEB-DL"
	SourceWEBRip Source = "WEBRip"
	SourceHDTV   Source = "HDTV"
)

type Codec string

const (
	CodecHEVC Codec = "HEVC"
	CodecH264 Codec = "H264"
	CodecAV1  Codec = "AV1"
)

type HDR string

const (
	HDRDolbyVision HDR = "DV"
	HDR10Plus      HDR = "HDR10+"
	HDR10          HDR = "HDR10"
	HDRSDR         HDR = "SDR"
)

// Profile represents observed quality facts for a release or library file.
// Invariant: quality properties are raw facts, not pre-baked global ranks.
type Profile struct {
	Resolution              Resolution `json:"resolution,omitempty"`
	ResolutionParserVersion string     `json:"resolution_parser_version,omitempty"`
	Source                  Source     `json:"source,omitempty"`
	SourceParserVersion     string     `json:"source_parser_version,omitempty"`
	Codec                   Codec      `json:"codec,omitempty"`
	CodecParserVersion      string     `json:"codec_parser_version,omitempty"`
	HDR                     HDR        `json:"hdr,omitempty"`
	HDRParserVersion        string     `json:"hdr_parser_version,omitempty"`
	Audio                   string     `json:"audio,omitempty"`
	Edition                 string     `json:"edition,omitempty"`
	Group                   string     `json:"group,omitempty"`
}

// Policy defines a versioned ranking strategy that assigns relative score to a quality profile.
type Policy struct {
	ID              string
	Revision        int64
	ResolutionOrder map[Resolution]int
	SourceOrder     map[Source]int
	PreferHDR       bool
}

func DefaultPolicy() Policy {
	return Policy{
		ID:       "default",
		Revision: 1,
		ResolutionOrder: map[Resolution]int{
			Res2160p: 400,
			Res1080p: 300,
			Res720p:  200,
			Res480p:  100,
		},
		SourceOrder: map[Source]int{
			SourceRemux:  50,
			SourceBluray: 40,
			SourceWEBDL:  30,
			SourceWEBRip: 20,
			SourceHDTV:   10,
		},
		PreferHDR: true,
	}
}

// Score computes the relative numerical score of a profile under this specific policy.
func (p Policy) Score(q Profile) int {
	score := 0
	if resScore, ok := p.ResolutionOrder[q.Resolution]; ok {
		score += resScore
	}
	if srcScore, ok := p.SourceOrder[q.Source]; ok && q.SourceParserVersion == SourceTokenParserVersion {
		score += srcScore
	}
	if p.PreferHDR && VerifiedHDR(q) && (q.HDR == HDRDolbyVision || q.HDR == HDR10Plus || q.HDR == HDR10) {
		score += 15
	}
	return score
}

// Compare returns > 0 if a > b under this policy, < 0 if a < b, 0 if equal.
func (p Policy) Compare(a, b Profile) int {
	return p.Score(a) - p.Score(b)
}

// ParseResolution extracts normalized Resolution enum from release string.
func ParseResolution(s string) Resolution {
	tokens := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	values := map[Resolution]bool{}
	for _, token := range tokens {
		switch token {
		case "2160p", "4k", "uhd":
			values[Res2160p] = true
		case "1080p", "fhd":
			values[Res1080p] = true
		case "720p":
			values[Res720p] = true
		case "480p":
			values[Res480p] = true
		case "1080i", "720i", "2160i", "480i":
			return ""
		}
	}
	if len(values) != 1 {
		return ""
	}
	for value := range values {
		return value
	}
	return ""

}
