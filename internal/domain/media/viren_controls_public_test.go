package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"strings"
	"testing"
)

func TestVirenBracketAndSuffixControls(t *testing.T) {
	for _, raw := range []string{
		"[GM-Team][国漫][未知 第3季][Unseen Comet][01-26][AVC][GB][1080P]",
		"【4月/悠哈璃羽字幕社】[UHA-WINGS][未知][Unseen Comet][04][1080p AVC_AAC][简繁外挂][sc_tc]",
		"【喵萌奶茶屋】★01月新番★[未知/Unseen Comet][01][1080p][繁體]",
	} {
		r, m := media.RecognizeRelease(raw), media.Recognize(raw)
		if r.Title != "Unseen Comet" || m.Title != "Unseen Comet" || len(r.UnitKeys) != 0 || m.Identity != nil {
			t.Fatalf("lexical view changed identity or failed %q: %+v / %+v", raw, r, m)
		}
	}
	for _, raw := range []string{
		"[Anipakku] Unseen Comet - Season 3 46.mkv [Unknown]",
		"[Unknown][国漫][未知 第3季][Unseen Comet][01-26][AVC][GB][1080P]",
		"【Unknown】★01月新番★[未知/Unseen Comet][01][1080p][繁體]",
		"Unseen Comet[unknown.ru].avi",
	} {
		if r := media.RecognizeRelease(raw); r.Title == "Unseen Comet" {
			t.Fatalf("unknown layout stripped: %+v", r)
		}
	}
}
func TestVirenYearsDatesAndCompleteLists(t *testing.T) {
	for _, raw := range []string{"Unseen.1984.2020.1080p.BluRay.x264.mkv", "Unseen.2024.03.15.1080p.WEB-DL.mkv"} {
		r := media.RecognizeRelease(raw)
		if r.Year != 0 || len(r.UnitKeys) != 0 || !r.RequiresReview {
			t.Fatalf("date/year authority changed %+v", r)
		}
	}
	raw := "Unseen.2024.PPV.Story.HDTV.x264-GRP"
	if r := media.RecognizeRelease(raw); r.Year != 2024 || r.Title != "Unseen" {
		t.Fatalf("year boundary changed %+v", r)
	}
	r := media.RecognizeRelease("Unseen.S02E01E03.1080p.WEB-DL.mkv")
	if len(r.UnitKeys) != 2 || r.UnitKeys[0] != "S02E01" || r.UnitKeys[1] != "S02E03" {
		t.Fatalf("complete list changed %+v", r)
	}
	r = media.RecognizeRelease("Unseen.US.S01E01.1080p.WEB-DL.mkv")
	if !strings.Contains(r.Title, "US") {
		t.Fatalf("regional identity stripped %+v", r)
	}
}
