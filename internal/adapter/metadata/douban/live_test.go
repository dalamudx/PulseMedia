package douban_test

import (
	"os"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/douban"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// TestLiveRankingsReadOnly makes one signed GET per ranking against the real
// service. It is skipped unless PULSE_DOUBAN_LIVE=1 and credentials are given
// through the environment; it never prints them.
func TestLiveRankingsReadOnly(t *testing.T) {
	if os.Getenv("PULSE_DOUBAN_LIVE") != "1" {
		t.Skip("set PULSE_DOUBAN_LIVE=1 with PULSE_DOUBAN_API_KEY and PULSE_DOUBAN_API_SECRET to run")
	}
	client := douban.NewClient("", nil, douban.WithCredentials(douban.Credentials{
		APIKey: os.Getenv("PULSE_DOUBAN_API_KEY"), Secret: os.Getenv("PULSE_DOUBAN_API_SECRET"),
	}))
	for _, list := range []string{ports.DoubanListHotMovie, ports.DoubanListHotTV, ports.DoubanListWeeklyChinese, ports.DoubanListWeeklyGlobal, ports.DoubanListTop250} {
		items, err := client.GetRanking(t.Context(), list, 0, 20)
		if err != nil {
			t.Fatalf("%s: %v", list, err)
		}
		withYear, withPoster, withRating := 0, 0, 0
		for _, item := range items {
			if item.Year > 0 {
				withYear++
			}
			if item.PosterURL != "" {
				withPoster++
			}
			if item.Rating > 0 {
				withRating++
			}
		}
		first := "-"
		if len(items) > 0 {
			first = items[0].Title
		}
		t.Logf("LIVE %s: items=%d year=%d poster=%d rating=%d type=%s first=%q", list, len(items), withYear, withPoster, withRating, map[bool]string{true: "ok", false: "-"}[len(items) > 0 && items[0].MediaType != ""], first)
		if len(items) == 0 {
			t.Fatalf("%s returned no items", list)
		}
	}
}
