package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestDottedInitialGroupDoesNotBecomeTechnicalTail(t *testing.T) {
	for _, suffix := range []string{"S.V", "S.V[ettv]", "S.V.mkv"} {
		p := media.RecognizeRelease("Example.2020.S01E01.1080p.HDTV.x264-" + suffix)
		if p.Quality.Group != "S.V" {
			t.Fatalf("explicit dotted initials lost: %+v", p.Quality)
		}
	}
	for _, name := range []string{"Example.2020.S01E01.1080p.WEB-DL.x264.AAC", "Example.2020.1080p.WEB-DL.H.264", "Example.2020.1080p.WEB-DL.x264", "Example.2020.1080p.HDTV.x264-S..V"} {
		if p := media.RecognizeRelease(name); p.Quality.Group != "" {
			t.Fatalf("technical or invalid tail certified as group: %s %+v", name, p.Quality)
		}
	}
}

func TestDottedReleaseInitialsRequireLiteralDots(t *testing.T) {
	for _, tc := range []struct{ tail, want string }{
		{"S.V", "S.V"}, {"B.E.N", "B.E.N"}, {"A.B.C.D.E.F.G.H", "A.B.C.D.E.F.G.H"},
		{"S V", ""}, {"S_V", ""}, {"BEN.THE.MEN", ""}, {"B E N", ""}, {"A.B.C.D.E.F.G.H.I", ""},
	} {
		got := media.RecognizeRelease("Fixture.2024.1080p.WEB-DL.x264-" + tc.tail)
		if got.Quality.Group != tc.want {
			t.Errorf("literal-dot group gate %q: got %q want %q", tc.tail, got.Quality.Group, tc.want)
		}
	}
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceWEBDL: 20}, RejectGroups: []string{"BEN.THE.MEN"}}
	parsed := media.RecognizeRelease("Fixture.2024.1080p.WEB-DL.x264-BEN.THE.MEN")
	if quality.EvaluateCandidate(rules, parsed.RawName, parsed.Quality).Passed {
		t.Fatal("unknown structured group bypassed raw-suffix rejection")
	}
}

func TestKnownRelayTagsDoNotAlterReleaseGroupOrPolicy(t *testing.T) {
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceHDTV: 20}, RejectGroups: []string{"ASAP"}}
	for _, tail := range []string{"ASAP[ettv]", "ASAP [EZTV]", "ASAP[ettv][rartv]", "ASAP[TGx]", "ASAP[RaRbG]", "ASAP[rarbg][rartv]"} {
		parsed := media.RecognizeRelease("Example.2020.S01E01.1080p.HDTV.x264-" + tail)
		if parsed.Quality.Group != "ASAP" || parsed.ParserVersion != media.EpisodeParserVersion {
			t.Fatalf("known relay became group identity: %+v", parsed)
		}
		if d := quality.EvaluateCandidate(rules, parsed.RawName, parsed.Quality); d.Passed {
			t.Fatal("known relay bypassed rejected group", tail, d)
		}
	}
	raw := media.RecognizeRelease("Example.2020.S01E01.1080p.HDTV.x264-ASAP[unreviewed]")
	if raw.Quality.Group != "ASAP[unreviewed]" {
		t.Fatal("unknown relay was silently certified", raw.Quality)
	}
	if d := quality.EvaluateCandidate(rules, raw.RawName, raw.Quality); d.Passed {
		t.Fatal("unknown decoration bypassed negative policy", d)
	}
	if p := media.RecognizeRelease("Example.2020.1080p.BrRip.x264").Quality; p.Source != quality.SourceBluray || p.SourceParserVersion != quality.SourceTokenParserVersion {
		t.Fatal("original Blu-ray source-family label lost", p)
	}
}

func TestUnknownUTRRemainsRawAndCannotHideDeniedGroup(t *testing.T) {
	p := media.RecognizeRelease("Fixture.2020.S05E02.1080p.WEB-DL.x265-Qman[UTR].mkv")
	if p.Quality.Group != "Qman[UTR]" {
		t.Fatal("unknown decoration must remain raw evidence, not certified relay", p.Quality)
	}
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceWEBDL: 20}, RejectGroups: []string{"Qman"}}
	if d := quality.EvaluateCandidate(rules, p.RawName, p.Quality); d.Passed || d.RejectedReason != "命中黑名单发布组: Qman" {
		t.Fatal("unknown tag bypassed negative group policy", d)
	}
	if p.Quality.Group != "Qman[UTR]" {
		t.Fatal("negative comparison rewrote raw group observation")
	}
	if quality.RejectedReleaseGroup("Qman2[UTR]", "Qman") {
		t.Fatal("unrelated group prefix wrongly denied")
	}
}

func TestUnknownDottedSiteTailCannotCertifyOrHideGroup(t *testing.T) {
	name := "Revenge.2008.720p.X264-DIMENSION [example.co.uk]"
	p := media.RecognizeRelease(name)
	if p.RawName != name || p.Quality.Group != "" {
		t.Fatal("site-like unknown suffix must preserve raw evidence without certified group", p)
	}
	// The original has no source evidence and must stop at quality_unknown.
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res720p: 200}, SourceOrder: map[quality.Source]int{quality.SourceWEBDL: 20}, RejectGroups: []string{"DIMENSION"}}
	if d := quality.EvaluateCandidate(rules, p.RawName, p.Quality); d.Passed || d.RejectedReason != "quality_unknown" {
		t.Fatal("unknown original source acquired quality authority", d)
	}
	// This separate behavior fixture explicitly supplies its own source token;
	// it is not a golden input and does not re-sign the original observation.
	probeName := "Fixture.2008.720p.WEB-DL.X264-DIMENSION [example.co.uk]"
	probe := media.RecognizeRelease(probeName)
	if probe.Quality.Group != "" || probe.RawName != probeName {
		t.Fatal("opaque site suffix became certified group identity", probe)
	}
	if d := quality.EvaluateCandidate(rules, probe.RawName, probe.Quality); d.Passed || d.RejectedReason != "命中黑名单发布组: DIMENSION" {
		t.Fatal("raw site-like decoration bypassed negative group policy", d)
	}
	if p.RawName != name || p.Quality.Group != "" {
		t.Fatal("negative comparison invented or rewrote a group fact", p)
	}
}
