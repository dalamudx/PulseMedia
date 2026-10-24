package im

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRobotImageCards(t *testing.T) {
	image := "https://image.tmdb.org/t/p/w342/owned-poster.jpg"
	for kind, destination := range destinations {
		t.Run(kind, func(t *testing.T) {
			artwork := Artwork{URL: image}
			if kind == "feishu" {
				artwork = Artwork{Key: "img_owned-key"}
			}
			calls := 0
			h := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				var p map[string]any
				if json.Unmarshal(raw, &p) != nil {
					t.Fatal("bad card")
				}
				switch kind {
				case "wechat":
					if p["msgtype"] != "news" || !strings.Contains(string(raw), `"picurl":"`+image+`"`) {
						t.Fatal("missing image article")
					}
				case "dingtalk":
					if !strings.Contains(string(raw), "![封面]("+image+")") {
						t.Fatal("missing markdown image")
					}
				case "feishu":
					if !strings.Contains(string(raw), `"img_key":"img_owned-key"`) {
						t.Fatal("missing image key")
					}
				case "bark":
					if p["icon"] != image {
						t.Fatal("missing icon")
					}
				}
				response := `{"errcode":0}`
				if kind == "feishu" {
					response = `{"code":0}`
				}
				if kind == "bark" {
					response = `{"code":200}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
			})}
			if e := NewClient(kind, destination, "", h, nil, artwork).Test(t.Context()); e != nil || calls != 1 {
				t.Fatal("image notification failed", e, calls)
			}
			if e := NewClient(kind, destination, "", h, nil, Artwork{URL: "https://private.invalid/image?token=private"}).Test(t.Context()); e == nil || calls != 1 {
				t.Fatal("unsafe image caused network IO")
			}
		})
	}
}
