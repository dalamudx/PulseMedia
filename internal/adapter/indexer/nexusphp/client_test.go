package nexusphp_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/nexusphp"
)

func TestNexusPHPSearchAndScraping(t *testing.T) {
	mockHTML := `
	<!DOCTYPE html>
	<html>
	<body>
	<table class="torrents">
	  <tr>
	    <td>类别</td><td>标题</td><td>大小</td><td>种子</td>
	  </tr>
	  <tr>
	    <td>电影</td>
	    <td>
	      <a href="details.php?id=12345" title="Fight Club 1999 1080p BluRay x264-SPARKS"><b>搏击俱乐部 / Fight Club (1999) [1080p]</b></a>
	      <br><a href="download.php?id=12345">[下载]</a>
	    </td>
	    <td>8.45 GB</td>
	    <td><b><a href="torrents.php?id=12345&dllist=1#seeders">48</a></b></td>
	  </tr>
	  <tr>
	    <td>剧集</td>
	    <td>
	      <a href="details.php?id=67890" title="Breaking Bad S01 1080p BluRay"><b>绝命毒师 第一季 / Breaking Bad S01</b></a>
	      <br><a href="download.php?id=67890">[下载]</a>
	    </td>
	    <td>15.20 GB</td>
	    <td><b><a href="torrents.php?id=67890&dllist=1#seeders">112</a></b></td>
	  </tr>
	</table>
	</body>
	</html>
	`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check cookie
		if r.Header.Get("Cookie") != "nexus_uid=123; nexus_pass=abc" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `<a href="login.php">Please login with password</a>`)
			return
		}

		if strings.Contains(r.URL.Path, "torrents.php") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, mockHTML)
			return
		}

		if strings.Contains(r.URL.Path, "download.php") {
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = fmt.Fprint(w, "d8:announce32:https://tracker.example.com/announcee")
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()

	// 1. Successful authenticated search
	client := nexusphp.NewClient(server.URL, "nexus_uid=123; nexus_pass=abc", "secret_passkey", server.Client())
	items, err := client.Search(ctx, "Fight Club", nil, 0, 20)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	item1 := items[0]
	if !strings.Contains(item1.Title, "Fight Club") {
		t.Fatalf("unexpected title: %s", item1.Title)
	}
	if !strings.Contains(item1.DownloadURL, "download.php?id=12345") || !strings.Contains(item1.DownloadURL, "secret_passkey") {
		t.Fatalf("unexpected dl url: %s", item1.DownloadURL)
	}
	if item1.SizeBytes < 8*1024*1024*1024 {
		t.Fatalf("expected size > 8GB, got %d", item1.SizeBytes)
	}

	// 2. Fetch torrent bytes
	bytes, err := client.FetchTorrent(ctx, item1.DownloadURL)
	if err != nil || len(bytes) == 0 {
		t.Fatalf("fetch torrent failed: %v", err)
	}

	// 3. Unauthenticated rejection
	badClient := nexusphp.NewClient(server.URL, "bad_cookie", "", server.Client())
	_, err = badClient.Search(ctx, "Fight Club", nil, 0, 20)
	if err == nil {
		t.Fatal("expected auth error with bad cookie")
	}
}
