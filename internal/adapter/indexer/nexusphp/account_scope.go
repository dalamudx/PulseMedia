package nexusphp

import (
	"bytes"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/net/html"
)

// parseAccountWelcome binds observations to the unique authenticated welcome row.
func parseAccountWelcome(body []byte, base *url.URL, expected string) (*html.Node, error) {
	bad := func() (*html.Node, error) { return nil, ports.ErrBadResponse }
	// Reject duplicate attributes before HTML's error recovery can hide contradictions.
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	tokens := 0
	stack := []string{}
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			if tokenizer.Err() != io.EOF {
				return bad()
			}
			break
		}
		tokens++
		if tokens > 32768 {
			return bad()
		}
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			raw := tokenizer.Raw()
			if len(raw) > 65536 || !accountTagAttributes(raw) {
				return bad()
			}
			name, _ := tokenizer.TagName()
			tag := string(name)
			if kind == html.StartTagToken && !strings.Contains("|area|base|br|col|embed|hr|img|input|link|meta|param|source|track|wbr|", "|"+tag+"|") {
				stack = append(stack, tag)
				if len(stack) > 256 {
					return bad()
				}
			}
		} else if kind == html.EndTagToken {
			name, _ := tokenizer.TagName()
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == string(name) {
					stack = stack[:i]
					break
				}
			}
		}
	}
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return bad()
	}
	var block *html.Node
	blocks := 0
	invalid := false
	accountWalk(root, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if accountAttr(n, "id") == "info_block" {
			blocks++
			block = n
		}
		id := strings.ToLower(accountAttr(n, "id"))
		if n.Data == "base" || (n.Data == "input" && strings.EqualFold(accountAttr(n, "type"), "password")) || strings.Contains(id, "challenge") || strings.HasPrefix(id, "cf-") {
			invalid = true
		}
	})
	if invalid || blocks != 1 || block.Data != "table" {
		return bad()
	}
	var welcome *html.Node
	logouts := 0
	accountWalk(block, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "a" {
			return
		}
		link, ok := accountLink(base, accountAttr(n, "href"))
		if !ok {
			invalid = true
			return
		}
		if link.Path == path.Join(path.Dir(base.Path), "logout.php") {
			if link.RawQuery != "" || link.Fragment != "" {
				invalid = true
			}
			logouts++
			welcome = accountAncestor(n, "tr")
		}
	})
	if invalid || logouts != 1 || welcome == nil {
		return bad()
	}
	ids := 0
	accountWalk(welcome, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "a" {
			return
		}
		link, ok := accountLink(base, accountAttr(n, "href"))
		if !ok {
			invalid = true
			return
		}
		if link.Path == path.Join(path.Dir(base.Path), "userdetails.php") {
			ids++
			query, e := url.ParseQuery(link.RawQuery)
			if e != nil || len(query) != 1 || len(query["id"]) != 1 || query.Get("id") != expected || !indexer.ValidSiteAccountID(query.Get("id")) || link.Fragment != "" {
				invalid = true
			}
		}
	})
	if invalid || ids != 1 {
		return bad()
	}
	return welcome, nil
}
