package tvdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const origin = "https://api4.thetvdb.com"
const responseLimit = 5 << 20
const episodeLimit = 5000

var _ ports.TVDBProvider = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	apiKey     string
	pin        string
}

func NewClient(apiKey string, client *http.Client) *Client {
	return NewClientWithPIN(apiKey, "", client)
}
func NewClientWithPIN(apiKey, pin string, client *http.Client) *Client {
	bounded := http.Client{Timeout: 10 * time.Second}
	if client != nil {
		bounded = *client
	}
	if bounded.Timeout <= 0 || bounded.Timeout > 10*time.Second {
		bounded.Timeout = 10 * time.Second
	}
	bounded.Jar = nil
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{httpClient: &bounded, apiKey: apiKey, pin: pin}
}

// One login per read operation; no persisted token or automatic authentication replay.
func (c *Client) login(ctx context.Context) (string, error) {
	if !credential(c.apiKey, 512) || (c.pin != "" && !credential(c.pin, 512)) {
		return "", ports.ErrAuthRequired
	}
	body, err := json.Marshal(struct {
		APIKey string `json:"apikey"`
		PIN    string `json:"pin,omitempty"`
	}{c.apiKey, c.pin})
	if err != nil {
		return "", ports.ErrBadResponse
	}
	data, err := c.request(ctx, http.MethodPost, "/v4/login", "", body)
	if err != nil {
		return "", err
	}
	var result struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(data.Data, &result) != nil || !credential(result.Token, 8192) {
		return "", ports.ErrBadResponse
	}
	return result.Token, nil
}

func (c *Client) GetSeriesDetails(ctx context.Context, id int64) (*ports.TVDBSeriesDetails, error) {
	if id <= 0 {
		return nil, ports.ErrRejected
	}
	token, err := c.login(ctx)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, http.MethodGet, "/v4/series/"+strconv.FormatInt(id, 10)+"/extended", token, nil)
	if err != nil {
		return nil, err
	}
	return decodeSeries(data.Data, id)
}

func (c *Client) GetSeriesTranslation(ctx context.Context, id int64, language string) (*ports.TVDBTranslation, error) {
	if id <= 0 || len(language) != 3 || strings.Trim(language, "abcdefghijklmnopqrstuvwxyz") != "" {
		return nil, ports.ErrRejected
	}
	token, err := c.login(ctx)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, http.MethodGet, "/v4/series/"+strconv.FormatInt(id, 10)+"/translations/"+language, token, nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Language string `json:"language"`
		Name     string `json:"name"`
		Overview string `json:"overview"`
	}
	if json.Unmarshal(data.Data, &raw) != nil || raw.Language != language || !text(raw.Name, 1024) || !text(raw.Overview, 65536) {
		return nil, ports.ErrBadResponse
	}
	return &ports.TVDBTranslation{TVDBID: id, Language: raw.Language, Title: raw.Name, Overview: raw.Overview}, nil
}

func (c *Client) GetSeasonMetadata(ctx context.Context, id int64, season int) (*ports.TVDBSeasonMetadata, error) {
	if id <= 0 || season < 0 || season > 1000 {
		return nil, ports.ErrRejected
	}
	token, err := c.login(ctx)
	if err != nil {
		return nil, err
	}
	path := "/v4/series/" + strconv.FormatInt(id, 10) + "/episodes/official"
	result := &ports.TVDBSeasonMetadata{TVDBID: id, SeasonNumber: season, Numbering: "official", Episodes: []ports.TVDBEpisode{}}
	ids, numbers := map[int64]bool{}, map[int]bool{}
	total := -1
	for page := 0; page < 64; page++ {
		data, e := c.request(ctx, http.MethodGet, path+"?page="+strconv.Itoa(page)+"&season="+strconv.Itoa(season), token, nil)
		if e != nil {
			return nil, e
		}
		episodes, next, count, e := decodePage(data, id, season, page, path)
		if e != nil {
			return nil, e
		}
		if count != nil {
			if total >= 0 && total != *count {
				return nil, ports.ErrBadResponse
			}
			total = *count
		}
		for _, episode := range episodes {
			if ids[episode.TVDBID] || numbers[episode.EpisodeNumber] {
				return nil, ports.ErrBadResponse
			}
			ids[episode.TVDBID], numbers[episode.EpisodeNumber] = true, true
			result.Episodes = append(result.Episodes, episode)
		}
		if len(result.Episodes) > episodeLimit || (total >= 0 && len(result.Episodes) > total) {
			return nil, ports.ErrBadResponse
		}
		if !next {
			if total >= 0 && len(result.Episodes) != total {
				return nil, ports.ErrBadResponse
			}
			result.Complete = true
			return result, nil
		}
		if len(episodes) == 0 {
			return nil, ports.ErrBadResponse
		}
	}
	return nil, ports.ErrBadResponse
}

type envelope struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Links  json.RawMessage `json:"links"`
}

func (c *Client) request(ctx context.Context, method, path, token string, body []byte) (*envelope, error) {
	req, err := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(body))
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PulseMedia/0.1.0 (https://github.com/dalamudx/PulseMedia)")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return nil, readError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if ctx.Err() != nil {
		return nil, readError(ctx, ctx.Err())
	}
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ports.ErrAuthRequired
	case http.StatusNotFound:
		return nil, ports.ErrNotFound
	case http.StatusTooManyRequests:
		return nil, ports.NewRateLimitError(retryAfter(response.Header), "TVDB request limited")
	default:
		if response.StatusCode >= 500 {
			return nil, ports.ErrUnavailable
		}
		return nil, ports.ErrBadResponse
	}
	if response.ContentLength > responseLimit {
		return nil, ports.ErrBadResponse
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return nil, readError(ctx, err)
	}
	if ctx.Err() != nil {
		return nil, readError(ctx, ctx.Err())
	}
	if len(raw) > responseLimit || !utf8.Valid(raw) || !uniqueJSON(raw) {
		return nil, ports.ErrBadResponse
	}
	var result envelope
	if json.Unmarshal(raw, &result) != nil || result.Status != "success" || len(result.Data) == 0 || bytes.Equal(result.Data, []byte("null")) {
		return nil, ports.ErrBadResponse
	}
	return &result, nil
}
func credential(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, ch := range value {
		if ch < '!' || ch > '~' {
			return false
		}
	}
	return true
}
func readError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	var timeout net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}
func retryAfter(h http.Header) time.Duration {
	if seconds, err := strconv.ParseInt(h.Get("Retry-After"), 10, 64); err == nil {
		if seconds >= 0 && seconds <= 86400 {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	until, e1 := http.ParseTime(h.Get("Retry-After"))
	now, e2 := http.ParseTime(h.Get("Date"))
	if e1 != nil || e2 != nil || until.Before(now) || until.Sub(now) > 24*time.Hour {
		return 0
	}
	return until.Sub(now)
}
