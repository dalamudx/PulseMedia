package notification

import (
	"net/url"
	"strings"
	"unicode"
)

func ValidKind(kind string) bool {
	switch kind {
	case "webhook", "telegram", "wechat", "dingtalk", "feishu", "bark":
		return true
	}
	return false
}

// Robot destinations are explicit administrative configuration; Bark may be self-hosted.
func ValidRobot(kind, destination, secret string) bool {
	if len(destination) > 4000 || len(secret) > 1000 || strings.IndexFunc(destination, unicode.IsControl) >= 0 || strings.IndexFunc(secret, unicode.IsControl) >= 0 {
		return false
	}
	u, e := url.Parse(destination)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.Scheme != "https" {
		return false
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return false
	}
	one := func(key string) bool {
		v := q[key]
		return len(q) == 1 && len(v) == 1 && token(v[0])
	}
	switch kind {
	case "wechat":
		return u.Host == "qyapi.weixin.qq.com" && u.Path == "/cgi-bin/webhook/send" && one("key") && secret == ""
	case "dingtalk":
		return u.Host == "oapi.dingtalk.com" && u.Path == "/robot/send" && one("access_token")
	case "feishu":
		return u.Host == "open.feishu.cn" && strings.HasPrefix(u.Path, "/open-apis/bot/v2/hook/") && token(strings.TrimPrefix(u.Path, "/open-apis/bot/v2/hook/")) && u.RawQuery == ""
	case "bark":
		return u.RawQuery == "" && token(strings.TrimPrefix(u.Path, "/")) && secret == ""
	}
	return false
}
func token(v string) bool {
	if v == "" || len(v) > 256 {
		return false
	}
	for _, r := range v {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_", r) {
			return false
		}
	}
	return true
}
