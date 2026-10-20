package indexer

// Protocol is the wire protocol used to query or subscribe to an indexer.
type Protocol string

const (
	ProtocolTorznab  Protocol = "torznab"
	ProtocolNexusPHP Protocol = "nexusphp"
	ProtocolRSS      Protocol = "rss"
)

// CategoryMapping pairs a numeric category code with a human-readable label.
type CategoryMapping struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Template represents a builtin preset configuration for a known PT/BT indexer.
type Template struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	Domain                string            `json:"domain"`
	Protocol              Protocol          `json:"protocol"`
	DefaultURL            string            `json:"default_url"`
	RecommendedCategories []int             `json:"recommended_categories"`
	CategoryMappings      []CategoryMapping `json:"category_mappings"`
	Description           string            `json:"description"`
	CredentialHelp        string            `json:"credential_help"`
}

// builtinTemplates contains 24 curated preset templates for mainstream PT/BT sites and aggregators.
var builtinTemplates = []Template{
	{
		ID:                    "prowlarr",
		Name:                  "Prowlarr (Torznab 聚合)",
		Domain:                "localhost:9696",
		Protocol:              ProtocolTorznab,
		DefaultURL:            "http://localhost:9696/1/api?v=1",
		RecommendedCategories: []int{2000, 5000},
		CategoryMappings: []CategoryMapping{
			{ID: 2000, Name: "电影 (Movies)"},
			{ID: 5000, Name: "剧集 (TV)"},
		},
		Description:    "Prowlarr 聚合器 Torznab API，通过单接口统一检索全量配置站点",
		CredentialHelp: "需填入 Prowlarr 的 API Key（在 Prowlarr 设置 -> 常规中查看）",
	},
	{
		ID:                    "jackett",
		Name:                  "Jackett (Torznab 聚合)",
		Domain:                "localhost:9117",
		Protocol:              ProtocolTorznab,
		DefaultURL:            "http://localhost:9117/api/v2.0/indexers/all/results/torznab/",
		RecommendedCategories: []int{2000, 5000},
		CategoryMappings: []CategoryMapping{
			{ID: 2000, Name: "电影 (Movies)"},
			{ID: 5000, Name: "剧集 (TV)"},
		},
		Description:    "Jackett 全局聚合或指定站点 Torznab 接口",
		CredentialHelp: "需填入 Jackett API Key（右上角查看）",
	},
	{
		ID:                    "mteam",
		Name:                  "M-Team (馒头)",
		Domain:                "kp.m-team.cc",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://kp.m-team.cc/",
		RecommendedCategories: []int{401, 402, 403, 404, 405},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
			{ID: 405, Name: "动画 (Anime)"},
		},
		Description:    "国内老牌头部综合 PT 站，影视资源极其丰富",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "hdsky",
		Name:                  "HDSky (天空)",
		Domain:                "hdsky.me",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://hdsky.me/",
		RecommendedCategories: []int{401, 402, 403, 404, 405},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
			{ID: 405, Name: "动漫 (Animation)"},
		},
		Description:    "高清影视 PT 站，原盘与官方压制资源丰富",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "chdbits",
		Name:                  "CHDBits (彩虹岛)",
		Domain:                "chdbits.co",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://chdbits.co/",
		RecommendedCategories: []int{401, 402, 403, 404, 405},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
			{ID: 405, Name: "动漫 (Animation)"},
		},
		Description:    "老牌资深高清影视发布站，CHD 压制组大本营",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "ttg",
		Name:                  "TTG (ToTheGlory)",
		Domain:                "totheglory.im",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://totheglory.im/",
		RecommendedCategories: []int{51, 52, 53, 54, 108},
		CategoryMappings: []CategoryMapping{
			{ID: 51, Name: "电影 (Movie)"},
			{ID: 52, Name: "美剧 (US/EU TV)"},
			{ID: 53, Name: "日韩剧 (JP/KR TV)"},
			{ID: 54, Name: "华语剧 (CN TV)"},
			{ID: 108, Name: "纪录片 (Doc)"},
		},
		Description:    "老牌影视与压制站，WiKi 压制组大本营",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "hdchina",
		Name:                  "HDChina (瓷器)",
		Domain:                "hdchina.org",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://hdchina.org/",
		RecommendedCategories: []int{401, 402, 403, 404, 405},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
			{ID: 405, Name: "动漫 (Animation)"},
		},
		Description:    "国内老牌高清影视 PT 站，HDWinG 原盘基地",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "ourbits",
		Name:                  "OurBits (我堡)",
		Domain:                "ourbits.club",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://ourbits.club/",
		RecommendedCategories: []int{401, 402, 403, 404, 405},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
			{ID: 405, Name: "动漫 (Animation)"},
		},
		Description:    "综合性高清影视 PT 站，Ao 与 PbK 压制组基地",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "opencd",
		Name:                  "OpenCD (皇后)",
		Domain:                "open.cd",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://open.cd/",
		RecommendedCategories: []int{501, 502, 503, 504},
		CategoryMappings: []CategoryMapping{
			{ID: 501, Name: "华语无损 (Chinese Lossless)"},
			{ID: 502, Name: "古典无损 (Classical Lossless)"},
			{ID: 503, Name: "原声音乐 (OST)"},
			{ID: 504, Name: "演唱会 (Live Concert)"},
		},
		Description:    "高品质无损音乐专业 PT 站",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "springwood",
		Name:                  "SpringWood (春木)",
		Domain:                "springwood.me",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://springwood.me/",
		RecommendedCategories: []int{401, 402, 403, 404},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
		},
		Description:    "高清综合影视资源站",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "hdhome",
		Name:                  "HDHome (家园)",
		Domain:                "hdhome.org",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://hdhome.org/",
		RecommendedCategories: []int{411, 412, 413, 414, 415},
		CategoryMappings: []CategoryMapping{
			{ID: 411, Name: "电影 (Movie)"},
			{ID: 412, Name: "电视剧 (TV Series)"},
			{ID: 413, Name: "综艺 (TV Show)"},
			{ID: 414, Name: "纪录片 (Documentary)"},
			{ID: 415, Name: "动漫 (Animation)"},
		},
		Description:    "综合高清影视 PT 社区",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "pterclub",
		Name:                  "PTerClub (猫站)",
		Domain:                "pterclub.com",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://pterclub.com/",
		RecommendedCategories: []int{401, 402, 403, 404, 405},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
			{ID: 405, Name: "动漫 (Animation)"},
		},
		Description:    "二次元与综合高清影视社区",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "soulvoice",
		Name:                  "SoulVoice (聆音)",
		Domain:                "soulvoice.club",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://soulvoice.club/",
		RecommendedCategories: []int{401, 402, 403, 404},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
		},
		Description:    "有声书、音乐与综合影视 PT 站",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "audiences",
		Name:                  "Audiences (观众)",
		Domain:                "audiences.me",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://audiences.me/",
		RecommendedCategories: []int{401, 402, 403, 404},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
		},
		Description:    "精品影视 PT 社区",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "haidan",
		Name:                  "HaiDan (海胆)",
		Domain:                "www.haidan.video",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://www.haidan.video/",
		RecommendedCategories: []int{401, 402, 403, 404},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
		},
		Description:    "特色微压与综合影视站",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "lemonhd",
		Name:                  "LemonHD (柠檬)",
		Domain:                "lemonhd.org",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://lemonhd.org/",
		RecommendedCategories: []int{401, 402, 403, 404, 405},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
			{ID: 405, Name: "动漫 (Animation)"},
		},
		Description:    "综合影视与动漫社区",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "keepfrds",
		Name:                  "KeepFrds (朋友)",
		Domain:                "pt.keepfrds.com",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://pt.keepfrds.com/",
		RecommendedCategories: []int{401, 402, 403, 404},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
		},
		Description:    "小体积压制与剧集收藏站点",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "discfan",
		Name:                  "DiscFan (碟粉)",
		Domain:                "discfan.net",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://discfan.net/",
		RecommendedCategories: []int{401, 402, 403, 404},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
		},
		Description:    "原盘与高清影视站",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "carpt",
		Name:                  "CarPT (车牌)",
		Domain:                "carpt.net",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://carpt.net/",
		RecommendedCategories: []int{401, 402, 403, 404},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
		},
		Description:    "综合影视与汽车文化社区",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "redleaves",
		Name:                  "RedLeaves (红叶)",
		Domain:                "redleaves.club",
		Protocol:              ProtocolNexusPHP,
		DefaultURL:            "https://redleaves.club/",
		RecommendedCategories: []int{401, 402, 403, 404},
		CategoryMappings: []CategoryMapping{
			{ID: 401, Name: "电影 (Movie)"},
			{ID: 402, Name: "电视剧 (TV Series)"},
			{ID: 403, Name: "综艺 (TV Show)"},
			{ID: 404, Name: "纪录片 (Documentary)"},
		},
		Description:    "综合高清影视社区",
		CredentialHelp: "需填入浏览器登录后的完整 Cookie",
	},
	{
		ID:                    "iptorrents",
		Name:                  "IPTorrents",
		Domain:                "iptorrents.com",
		Protocol:              ProtocolTorznab,
		DefaultURL:            "https://iptorrents.com/torznab/api",
		RecommendedCategories: []int{2000, 5000},
		CategoryMappings: []CategoryMapping{
			{ID: 2000, Name: "电影 (Movies)"},
			{ID: 5000, Name: "剧集 (TV)"},
		},
		Description:    "国外老牌综合 PT 站，提供标准 Torznab 接口",
		CredentialHelp: "需填入 IPTorrents 账号设置中的 API Key / RSS Key",
	},
	{
		ID:                    "torrentleech",
		Name:                  "TorrentLeech",
		Domain:                "torrentleech.org",
		Protocol:              ProtocolTorznab,
		DefaultURL:            "https://torrentleech.org/torznab/api",
		RecommendedCategories: []int{2000, 5000},
		CategoryMappings: []CategoryMapping{
			{ID: 2000, Name: "电影 (Movies)"},
			{ID: 5000, Name: "剧集 (TV)"},
		},
		Description:    "国外大型综合 PT 站，0-day 资源丰富",
		CredentialHelp: "需填入 TorrentLeech Profile 中的 API Key",
	},
	{
		ID:                    "filelist",
		Name:                  "FileList",
		Domain:                "filelist.io",
		Protocol:              ProtocolTorznab,
		DefaultURL:            "https://filelist.io/torznab/api",
		RecommendedCategories: []int{2000, 5000},
		CategoryMappings: []CategoryMapping{
			{ID: 2000, Name: "电影 (Movies)"},
			{ID: 5000, Name: "剧集 (TV)"},
		},
		Description:    "欧洲著名综合 PT 站",
		CredentialHelp: "需填入 FileList 个人中心生成的 API Key / Passkey",
	},
	{
		ID:                    "1337x",
		Name:                  "1337x (RSS)",
		Domain:                "1337x.to",
		Protocol:              ProtocolRSS,
		DefaultURL:            "https://1337x.to/rss/featured/",
		RecommendedCategories: []int{2000, 5000},
		CategoryMappings: []CategoryMapping{
			{ID: 2000, Name: "电影 (Movies)"},
			{ID: 5000, Name: "剧集 (TV)"},
		},
		Description:    "国外著名公开 BT 站点精选资源 RSS 源",
		CredentialHelp: "公开 RSS 源，无需填写认证凭据",
	},
}

// BuiltinTemplates returns a defensive copy of all builtin indexer templates.
func BuiltinTemplates() []Template {
	out := make([]Template, len(builtinTemplates))
	for i, t := range builtinTemplates {
		out[i] = t
		out[i].RecommendedCategories = append([]int(nil), t.RecommendedCategories...)
		out[i].CategoryMappings = append([]CategoryMapping(nil), t.CategoryMappings...)
	}
	return out
}

// FindTemplate searches for a builtin template by its exact identifier.
func FindTemplate(id string) (Template, bool) {
	for _, t := range builtinTemplates {
		if t.ID == id {
			clone := t
			clone.RecommendedCategories = append([]int(nil), t.RecommendedCategories...)
			clone.CategoryMappings = append([]CategoryMapping(nil), t.CategoryMappings...)
			return clone, true
		}
	}
	return Template{}, false
}
