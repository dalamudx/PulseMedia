package bangumi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/bangumi"
)

func TestBangumiCalendarParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/calendar" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[
				{
					"weekday": {"id": 1, "cn": "星期一"},
					"items": [
						{
							"id": 12345,
							"name": "葬送のフリーレン",
							"name_cn": "葬送的芙莉莲",
							"air_date": "2023-09-29",
							"images": {"large": "https://lain.bgm.tv/pic/cover/l/frieren.jpg"},
							"rating": {"score": 8.9}
						}
					]
				}
			]`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := bangumi.NewClient(server.URL, server.Client())
	ctx := context.Background()

	sched, err := client.GetCalendar(ctx)
	if err != nil || len(sched) != 1 {
		t.Fatalf("get calendar failed: %v, len=%d", err, len(sched))
	}

	day := sched[0]
	if day.DayName != "星期一" || len(day.Items) != 1 {
		t.Fatalf("unexpected day: %+v", day)
	}

	anime := day.Items[0]
	if anime.Title != "葬送的芙莉莲" || anime.Rating != 8.9 {
		t.Fatalf("unexpected anime item: %+v", anime)
	}
}
