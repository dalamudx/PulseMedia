package media_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func assertPrivateGroup(t *testing.T, input, want string) {
	t.Helper()
	a, b := media.Recognize(input), media.RecognizeRelease(input)
	if a.RawName != input || b.RawName != input || a.Quality.Group != want || b.Quality.Group != want {
		t.Errorf("%q: movie group=%q release group=%q want=%q", input, a.Quality.Group, b.Quality.Group, want)
	}
}

func TestPrivatePublisherOriginalSix(t *testing.T) {
	for _, tc := range []struct{ input, group string }{
		{"Cloud.Atlas.2012.720p.BluRay.x264-BiDA [PublicHD]", "BiDA"},
		{"Iron.Man.3.2013.720p.WEB-DL.H264-WEBiOS [PublicHD]", "WEBiOS"},
		{"Man of Tai Chi 2013 720p Webrip x264 AC3-FooKaS (SilverTorrent)", "FooKaS"},
		{"Scott.Pilgrim.vs.The.World.2010.720p.x264-F", "F"},
		{"The.Best.Exotic.Marigold.Hotel.2011.720p.BluRay.X264-AMIABLE [PublicHD]", "AMIABLE"},
		{"World.War.Z.2013.Unrated.Cut.720p.BluRay.DTS.x264-WiKi [PublicHD]", "WiKi"},
	} {
		t.Run(tc.group, func(t *testing.T) { assertPrivateGroup(t, tc.input, tc.group) })
	}
}

func TestPrivatePublisherUnseenAndUnknown(t *testing.T) {
	for i, tail := range []struct{ raw, want string }{
		{"QUARTZ[PublicHD]", "QUARTZ"}, {"QUARTZ [pUbLiChD]", "QUARTZ"},
		{"QUARTZ (SilverTorrent)", "QUARTZ"}, {"QUARTZ(silvertorrent)[PublicHD]", "QUARTZ"},
		{"QUARTZ[PublicHD](SilverTorrent)", "QUARTZ"}, {"PublicHD", "PublicHD"},
		{"SilverTorrent", "SilverTorrent"}, {"QUARTZ[Uncatalogued]", "QUARTZ[Uncatalogued]"},
		{"QUARTZ [Uncatalogued]", ""}, {"QUARTZ (Uncatalogued)", ""},
		{"QUARTZ[Uncatalogued][PublicHD]", "QUARTZ[Uncatalogued]"},
		{"QUARTZ[PublicHD][Uncatalogued]", "QUARTZ[PublicHD][Uncatalogued]"},
		{"QUARTZ[PublicHDish]", "QUARTZ[PublicHDish]"}, {"QUARTZ[PublicHD", "QUARTZ[PublicHD"},
		{"QUARTZ (SilverTorrentish)", ""}, {"QUARTZ PublicHD", ""},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) { assertPrivateGroup(t, "Aster.Harbor.2023.1080p.BluRay.x264-"+tail.raw, tail.want) })
	}
	for _, suffix := range []string{"[PublicHD]", "(SilverTorrent)"} {
		t.Run(suffix, func(t *testing.T) { assertPrivateGroup(t, "Aster.Harbor.2023.1080p.BluRay.x264 "+suffix, "") })
	}
}

func TestPrivateSingletonTechnicalBoundary(t *testing.T) {
	for _, input := range []string{"Aster.Harbor.2023.720p.x264-F", "Aster.Harbor.2023.1080p.WEB-DL.x265-Q[PublicHD]", "Aster.Harbor.2023.1080p.BluRay.h264-Z.mkv"} {
		t.Run(input, func(t *testing.T) {
			want := "F"
			if input[len(input)-3:] == "mkv" {
				want = "Z"
			} else if input[len(input)-1:] == "]" {
				want = "Q"
			}
			assertPrivateGroup(t, input, want)
		})
	}
	for _, input := range []string{"Aster Harbor - F", "Aster.Harbor.2023-F", "Aster.Harbor.720p.x264-F", "Aster.Harbor.2023.x264-F", "Aster.Harbor.2023.720p-F", "Aster.Harbor.2023.720p.x264 - F", "Aster.Harbor.2023.720p.x264-f", "Aster.Harbor.2023.720p.x264-F.notes", "Aster.Harbor.2023.720p.1080p.x264-F", "Aster.Harbor.2023.720p.x264.x265-F", "Aster.Harbor.2022.2023.720p.x264-F", "Aster.Harbor.2023.720p.x264-F (Uncatalogued)"} {
		t.Run(input, func(t *testing.T) { assertPrivateGroup(t, input, "") })
	}
}

func TestPrivateOrdinaryTitlesAndYears(t *testing.T) {
	for _, title := range []string{"Aster Harbor - F", "Aster Harbor (Uncatalogued)", "Aster Harbor [Uncatalogued]", "Aster Harbor [PublicHD]", "Aster Harbor (SilverTorrent)", "Aster Harbor - Q"} {
		t.Run(title, func(t *testing.T) {
			a, b := media.Recognize(title), media.RecognizeRelease(title)
			if a.Title != title || b.Title != title || a.Year != 0 || b.Year != 0 {
				t.Errorf("title changed: movie=%+v release=%+v", a, b)
			}
			assertPrivateGroup(t, title, "")
		})
	}
	for _, year := range []string{"2022.2023", "2023.2024"} {
		t.Run(year, func(t *testing.T) {
			input := "Aster.Harbor." + year + ".1080p.BluRay.x264-F[PublicHD]"
			a, b := media.Recognize(input), media.RecognizeRelease(input)
			if a.Year != 0 || b.Year != 0 || !a.RequiresReview || !b.RequiresReview {
				t.Errorf("year ambiguity lost: %+v %+v", a, b)
			}
			assertPrivateGroup(t, input, "")
		})
	}
}

func TestPrivateTelevisionAndSlash(t *testing.T) {
	for _, tail := range []string{"[PublicHD]", "(SilverTorrent)", "[PublicHD](SilverTorrent)"} {
		t.Run(tail, func(t *testing.T) {
			input := "Aster.Harbor.S02E03.1080p.WEB-DL.x264-QUARTZ " + tail
			a := media.RecognizeRelease(input)
			if a.Title != "Aster Harbor" || a.Year != 0 || a.Season != 2 || !slices.Equal(a.Episodes, []int{3}) || !slices.Equal(a.UnitKeys, []string{"S02E03"}) || a.RequiresReview || a.Confidence != .95 {
				t.Errorf("TV changed: %+v", a)
			}
			assertPrivateGroup(t, input, "QUARTZ")
		})
	}
	for i, input := range []string{"Aster.S01E01/Aster.2023.1080p.BluRay.x264-F[PublicHD]", "Aster.S01E01\\Aster.2023.1080p.BluRay.x264-F[PublicHD]", "Aster.S01E01/Aster.S02E03.1080p.WEB-DL.x264-QUARTZ (SilverTorrent)", "Aster.S01E01\\Aster.S02E03.1080p.WEB-DL.x264-QUARTZ (SilverTorrent)"} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			a, b := media.Recognize(input), media.RecognizeRelease(input)
			if !a.RequiresReview || !b.RequiresReview || a.Confidence != 0 || b.Confidence != 0 || a.Identity != nil || len(b.UnitKeys) != 0 || len(b.Episodes) != 0 || b.AbsoluteEpisode != 0 {
				t.Errorf("parent coverage authorized: %+v %+v", a, b)
			}
		})
	}
}
