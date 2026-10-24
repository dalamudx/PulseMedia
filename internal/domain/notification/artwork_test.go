package notification

import "testing"

func TestChannelArtwork(t *testing.T) {
	image := "https://image.tmdb.org/t/p/w342/owned-poster.jpg"
	for _, kind := range []string{"wechat", "dingtalk", "bark"} {
		if !ValidArtwork(kind, image, "") || !ValidArtwork(kind, "", "") {
			t.Fatal(kind)
		}
	}
	if !ValidArtwork("feishu", "", "img_owned-key") || !ValidArtwork("feishu", "", "") {
		t.Fatal("existing image key rejected")
	}
	for _, c := range []struct{ kind, image, key string }{{"feishu", image, ""}, {"telegram", image, ""}, {"webhook", image, ""}, {"wechat", image, "img_owned"}, {"feishu", "", "not_an_image"}, {"feishu", "", "img_bad/escape"}, {"bark", "https://image.tmdb.org/t/p/w342/owned.jpg?token=private", ""}, {"bark", "https://image.tmdb.org.evil/t/p/w342/owned.jpg", ""}, {"bark", "https://image.tmdb.org/t/p/w342/../owned.jpg", ""}, {"bark", "http://image.tmdb.org/t/p/w342/owned.jpg", ""}, {"bark", "https://image.tmdb.org/t/p/w342/%6fwned.jpg", ""}, {"bark", "https://image.tmdb.org/t/p/w342/owned.jpg#private", ""}} {
		if ValidArtwork(c.kind, c.image, c.key) {
			t.Fatal("unsafe artwork accepted", c.kind)
		}
	}
}
