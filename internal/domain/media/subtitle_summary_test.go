package media_test

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestChineseSRTSummaryCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		count      int
		start, end int64
	}{
		{"unordered overlapping cues", "9\n00:00:05,125 --> 00:00:09,999\n你好\n\n4\n00:00:00,000 --> 00:00:07,500\n世界\n\n", 2, 0, 9999},
		{"maximum timestamp", "1\n99:59:59,999 --> 99:59:59,999\n终\n\n", 1, 359999999, 359999999},
		{"BOM CRLF and markup", "\ufeff1\r\n00:00:01,001 --> 00:00:02,999\r\n<b>中文</b>\r\n第二行\r\n\r\n", 1, 1001, 2999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := media.SummarizeChineseSRT([]byte(tc.body))
			if err != nil || got.CueCount != tc.count || got.StartMS != tc.start || got.EndMS != tc.end {
				t.Fatal(got, err)
			}
		})
	}
	for _, body := range []string{"", "1\n00:00:03,000 --> 00:00:01,000\n中文\n", "1\n00:00:01,000 --> 00:00:02,000\n<script>中文</script>\n"} {
		got, err := media.SummarizeChineseSRT([]byte(body))
		if !errors.Is(err, media.ErrInvalidSubtitle) || got != (media.SubtitleSummary{}) {
			t.Fatal("invalid summary", got, err)
		}
	}
}
