package notification

import "testing"

func TestRobotDestinations(t *testing.T) {
	cases := []struct {
		kind, url, secret string
		valid             bool
	}{
		{"wechat", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=owned", "", true},
		{"dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=owned", "SEC-owned", true},
		{"dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=owned", "", true},
		{"feishu", "https://open.feishu.cn/open-apis/bot/v2/hook/owned-uuid", "owned", true},
		{"bark", "https://api.day.app/owned-key", "", true},
		{"bark", "https://push.example:8443/owned-key", "", true},
		{"bark", "http://push.example/owned-key", "", false},
		{"wechat", "https://qyapi.weixin.qq.com.evil/cgi-bin/webhook/send?key=owned", "", false},
		{"wechat", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x&key=y", "", false},
		{"wechat", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x&extra=y", "", false},
		{"wechat", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=", "", false},
		{"wechat", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x", "unexpected", false},
		{"wechat", "https://qyapi.weixin.qq.com:443/cgi-bin/webhook/send?key=x", "", false},
		{"wechat", "https://user:password@qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x", "", false},
		{"dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=owned&timestamp=1&sign=x", "", false},
		{"feishu", "https://open.feishu.cn/open-apis/bot/v2/hook/../../bad", "", false},
		{"feishu", "https://open.feishu.cn/open-apis/bot/v2/hook/owned?x=1", "", false},
		{"bark", "https://push.example/a/b", "", false},
		{"bark", "https://push.example/%61", "", false},
		{"bark", "https://push.example/owned#fragment", "", false},
		{"bark", "https://push.example/owned", "secret", false},
		{"unknown", "https://push.example/owned", "", false},
		{"dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=owned", "SEC\nprivate", false},
	}
	for _, c := range cases {
		t.Run(c.kind+"/"+c.url, func(t *testing.T) {
			if ValidRobot(c.kind, c.url, c.secret) != c.valid {
				t.Fatal("wrong destination classification")
			}
		})
	}
}
func TestChannelKinds(t *testing.T) {
	for _, kind := range []string{"webhook", "telegram", "wechat", "dingtalk", "feishu", "bark"} {
		if !ValidKind(kind) {
			t.Fatal(kind)
		}
	}
	for _, kind := range []string{"", "Telegram", "im", "webpush"} {
		if ValidKind(kind) {
			t.Fatal(kind)
		}
	}
}
