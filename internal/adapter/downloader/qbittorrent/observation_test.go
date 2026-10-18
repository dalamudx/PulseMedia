package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"strings"
	"testing"
)

func observationRow(hash string) map[string]any {
	return map[string]any{"hash": hash, "save_path": "/downloads", "content_path": "/downloads/one.mkv", "progress": 0.5, "size": int64(32), "downloaded": int64(16), "state": "downloading"}
}
func observationClient(t *testing.T, body string, calls *int) *Client {
	t.Helper()
	c, e := NewClient("https://fixture.invalid", "fixture", "fixture", &http.Client{Transport: contractTransport(func(*http.Request) (*http.Response, error) {
		if calls != nil {
			*calls++
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	c.loggedIn = true
	return c
}
func TestQBObservationRejectsAmbiguousIdentityAndMissingFacts(t *testing.T) {
	hash := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	for _, tc := range []struct {
		name string
		rows []map[string]any
	}{{"wrong-hash", []map[string]any{observationRow(other)}}, {"two-results", []map[string]any{observationRow(hash), observationRow(other)}}, {"duplicate", []map[string]any{observationRow(hash), observationRow(hash)}}, {"invalid-hash", []map[string]any{observationRow("all")}}} {
		t.Run(tc.name, func(t *testing.T) {
			body, e := json.Marshal(tc.rows)
			if e != nil {
				t.Fatal(e)
			}
			c := observationClient(t, string(body), nil)
			info, e := c.GetTorrent(context.Background(), hash)
			if !errors.Is(e, ports.ErrBadResponse) || info != nil {
				t.Fatalf("accepted identity: %v", e)
			}
		})
	}
	for _, field := range []string{"hash", "save_path", "progress", "size", "downloaded", "state"} {
		for _, null := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/null=%t", field, null), func(t *testing.T) {
				row := observationRow(hash)
				if null {
					row[field] = nil
				} else {
					delete(row, field)
				}
				body, e := json.Marshal([]map[string]any{row})
				if e != nil {
					t.Fatal(e)
				}
				for _, list := range []bool{false, true} {
					c := observationClient(t, string(body), nil)
					var err error
					if list {
						_, err = c.ListTorrents(context.Background(), "")
					} else {
						_, err = c.GetTorrent(context.Background(), hash)
					}
					if !errors.Is(err, ports.ErrBadResponse) {
						t.Fatalf("missing %s accepted: %v", field, err)
					}
				}
			})
		}
	}
	for _, field := range []string{"size", "downloaded", "progress", "save_path", "state"} {
		t.Run("invalid-"+field, func(t *testing.T) {
			row := observationRow(hash)
			row[field] = int64(-1)
			if field == "progress" {
				row[field] = 1.1
			}
			if field == "save_path" || field == "state" {
				row[field] = ""
			}
			body, e := json.Marshal([]map[string]any{row})
			if e != nil {
				t.Fatal(e)
			}
			c := observationClient(t, string(body), nil)
			_, e = c.GetTorrent(context.Background(), hash)
			if !errors.Is(e, ports.ErrBadResponse) {
				t.Fatalf("invalid field accepted: %v", e)
			}
		})
	}
}
func TestQBObservationPrecisionAndValidHashControls(t *testing.T) {
	for _, hash := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		for _, progress := range []string{"0.99999999", "0.999999999999999999999", "1", "0"} {
			t.Run(fmt.Sprintf("%d/%s", len(hash), progress), func(t *testing.T) {
				body := fmt.Sprintf(`[{"hash":%q,"save_path":"/downloads","progress":%s,"size":9007199254740993,"downloaded":9007199254740992,"state":"downloading"}]`, strings.ToUpper(hash), progress)
				c := observationClient(t, body, nil)
				info, e := c.GetTorrent(context.Background(), strings.ToUpper(hash))
				if e != nil || info == nil {
					t.Fatal(e)
				}
				if info.InfoHash != hash || info.TotalBytes != 9007199254740993 || info.DownloadedBytes != 9007199254740992 {
					t.Fatal("hash or byte identity altered")
				}
				if strings.HasPrefix(progress, "0.") && info.Progress >= 1 {
					t.Fatal("partial progress promoted to complete")
				}
				if progress == "1" && info.Progress != 1 {
					t.Fatal("exact complete progress lost")
				}
			})
		}
	}
}
func TestQBGetRemoveRejectInvalidHashesBeforeLogin(t *testing.T) {
	for _, hash := range []string{"", "all", "aaa", strings.Repeat("g", 40), strings.Repeat("a", 40) + "|" + strings.Repeat("b", 40)} {
		t.Run(hash, func(t *testing.T) {
			calls := 0
			c := observationClient(t, "[]", &calls)
			c.loggedIn = false
			_, e := c.GetTorrent(context.Background(), hash)
			if !errors.Is(e, ports.ErrBadResponse) {
				t.Fatalf("Get invalid %v", e)
			}
			e = c.RemoveTorrent(context.Background(), hash, true)
			if !errors.Is(e, ports.ErrBadResponse) || calls != 0 {
				t.Fatal("invalid hash reached network", e)
			}
		})
	}
}
func TestQBListRejectsDuplicateHashIdentity(t *testing.T) {
	hash := strings.Repeat("a", 40)
	body, e := json.Marshal([]map[string]any{observationRow(hash), observationRow(strings.ToUpper(hash))})
	if e != nil {
		t.Fatal(e)
	}
	c := observationClient(t, string(body), nil)
	rows, e := c.ListTorrents(context.Background(), "")
	if !errors.Is(e, ports.ErrBadResponse) || rows != nil {
		t.Fatalf("duplicate list accepted %v", e)
	}
}

func TestQBObservationRejectsInvalidRawProgress(t *testing.T) {
	for _, progress := range []string{`"1"`, `null`, `1.00000000000000000001`, `-1e-1000000000`, `1e1000000000`} {
		t.Run(progress, func(t *testing.T) {
			body := fmt.Sprintf(`[{"hash":%q,"save_path":"/downloads","progress":%s,"size":1,"downloaded":1,"state":"uploading"}]`, strings.Repeat("a", 40), progress)
			c := observationClient(t, body, nil)
			_, e := c.GetTorrent(context.Background(), strings.Repeat("a", 40))
			if !errors.Is(e, ports.ErrBadResponse) {
				t.Fatalf("invalid raw progress accepted: %v", e)
			}
		})
	}
}
