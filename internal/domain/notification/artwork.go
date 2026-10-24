package notification

import (
	"net/url"
	"strings"
)

// Preserve historical messages: artwork is optional immutable channel configuration.
// Never accept private download links or an arbitrary tracking URL as a public image.
func ValidArtwork(kind, image, key string) bool {
	if kind == "feishu" {
		return image == "" && (key == "" || strings.HasPrefix(key, "img_") && token(key))
	}
	if key != "" {
		return false
	}
	if image == "" {
		return true
	}
	if kind != "wechat" && kind != "dingtalk" && kind != "bark" {
		return false
	}
	if len(image) > 1024 {
		return false
	}
	u, e := url.Parse(image)
	if e != nil || u.Scheme != "https" || u.Host != "image.tmdb.org" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, "/t/p/w342/") {
		return false
	}
	file := strings.TrimPrefix(u.Path, "/t/p/w342/")
	for _, suffix := range []string{".jpg", ".png", ".webp"} {
		if strings.HasSuffix(file, suffix) {
			return token(strings.TrimSuffix(file, suffix))
		}
	}
	return false
}
