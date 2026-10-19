package transmission

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// AutomaticTVObserver grants only native observation, never send or manual completion authority.
func (c *Client) AutomaticTVObserver() ports.AutomaticTVReadClient {
	return automaticTVReader{client: c}
}

type automaticTVReader struct{ client *Client }

var _ ports.AutomaticTVReadClient = automaticTVReader{}

type automaticTVNativeTask struct {
	Hash      *string                  `json:"hashString"`
	Directory *string                  `json:"downloadDir"`
	Labels    *[]string                `json:"labels"`
	Status    *int64                   `json:"status"`
	Total     *int64                   `json:"totalSize"`
	Valid     *int64                   `json:"haveValid"`
	Error     *int64                   `json:"error"`
	Files     *[]automaticTVNativeFile `json:"files"`
	Stats     *[]automaticTVNativeStat `json:"fileStats"`
}

type automaticTVNativeFile struct {
	Name      *string `json:"name"`
	Size      *int64  `json:"length"`
	Completed *int64  `json:"bytesCompleted"`
}

type automaticTVNativeStat struct {
	Completed *int64 `json:"bytesCompleted"`
	Wanted    *bool  `json:"wanted"`
}

func (r automaticTVReader) ListAutomaticTVTasks(ctx context.Context, tag string) ([]ports.AutomaticTVRemoteTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const prefix = "pulsemedia:op:"
	id, err := uuid.Parse(strings.TrimPrefix(tag, prefix))
	if err != nil || id == uuid.Nil || tag != prefix+id.String() {
		return nil, ports.ErrUnavailable
	}
	tasks, err := r.client.automaticTVGet(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]ports.AutomaticTVRemoteTask, 0)
	seen := make(map[string]bool, len(tasks))
	for _, native := range tasks {
		task, err := native.remote()
		if err != nil || seen[task.InfoHash] {
			return nil, ports.ErrUnavailable
		}
		seen[task.InfoHash] = true
		for _, label := range task.Tags {
			if label == tag {
				out = append(out, task)
				break
			}
		}
	}
	return out, nil
}

func (r automaticTVReader) GetAutomaticTVFiles(ctx context.Context, hash string) ([]ports.AutomaticTVRemoteFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !automaticTVHash(hash) {
		return nil, ports.ErrUnavailable
	}
	tasks, err := r.client.automaticTVGet(ctx, strings.ToLower(hash))
	if err != nil {
		return nil, err
	}
	if len(tasks) != 1 {
		return nil, ports.ErrUnavailable
	}
	native := tasks[0]
	task, err := native.remote()
	if err != nil || task.InfoHash != strings.ToLower(hash) || native.Files == nil || native.Stats == nil {
		return nil, ports.ErrUnavailable
	}
	files, stats := *native.Files, *native.Stats
	if len(files) == 0 || len(files) > 4096 || len(files) != len(stats) {
		return nil, ports.ErrUnavailable
	}
	out := make([]ports.AutomaticTVRemoteFile, 0, len(files))
	names := make(map[string]bool, len(files))
	var total, completed int64
	for i, f := range files {
		s := stats[i]
		if f.Name == nil || f.Size == nil || f.Completed == nil || s.Completed == nil || s.Wanted == nil {
			return nil, ports.ErrUnavailable
		}
		if !automaticTVPath(*f.Name, false) || *f.Size < 0 || *f.Completed < 0 || *f.Completed > *f.Size || *f.Completed != *s.Completed || total > math.MaxInt64-*f.Size {
			return nil, ports.ErrUnavailable
		}
		name := automaticTVFold(*f.Name)
		if names[name] {
			return nil, ports.ErrUnavailable
		}
		names[name] = true
		total += *f.Size
		completed += *f.Completed // bounded by total
		out = append(out, ports.AutomaticTVRemoteFile{Index: i, Path: *f.Name, Size: *f.Size, CompletedBytes: *f.Completed, Wanted: *s.Wanted})
	}
	for name := range names {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if names[parent] {
				return nil, ports.ErrUnavailable
			}
		}
	}
	if total != task.TotalBytes || completed < task.VerifiedBytes {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}

func (t automaticTVNativeTask) remote() (ports.AutomaticTVRemoteTask, error) {
	if t.Hash == nil || t.Directory == nil || t.Labels == nil || t.Status == nil || t.Total == nil || t.Valid == nil || t.Error == nil {
		return ports.AutomaticTVRemoteTask{}, ports.ErrUnavailable
	}
	if !automaticTVHash(*t.Hash) || !automaticTVPath(*t.Directory, true) || *t.Status < 0 || *t.Status > 6 || *t.Total < 0 || *t.Valid < 0 || *t.Valid > *t.Total || *t.Error < 0 || *t.Error > 3 {
		return ports.AutomaticTVRemoteTask{}, ports.ErrUnavailable
	}
	labels := *t.Labels
	seen := make(map[string]bool, len(labels))
	for _, label := range labels {
		if !automaticTVText(label) || strings.TrimSpace(label) != label || seen[label] {
			return ports.AutomaticTVRemoteTask{}, ports.ErrUnavailable
		}
		seen[label] = true
	}
	state := [...]string{"stopped", "checking", "checking", "queued", "downloading", "queued", "seeding"}[*t.Status]
	if *t.Error != 0 {
		state = "error"
	}
	// Download/check states cannot certify completion, even if byte counters agree.
	complete := *t.Error == 0 && *t.Total > 0 && *t.Valid == *t.Total && (*t.Status == 0 || *t.Status == 5 || *t.Status == 6)
	return ports.AutomaticTVRemoteTask{InfoHash: strings.ToLower(*t.Hash), SavePath: *t.Directory, State: state, Tags: labels, TotalBytes: *t.Total, VerifiedBytes: *t.Valid, Complete: complete}, nil
}

func automaticTVHash(hash string) bool {
	if len(hash) != 40 {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func automaticTVText(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func automaticTVPath(s string, absolute bool) bool {
	return automaticTVText(s) && len(s) <= 4096 && path.IsAbs(s) == absolute && path.Clean(s) == s && s != "." && s != ".." && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\\:")
}

func automaticTVFold(s string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < smallest {
				smallest = next
			}
		}
		return smallest
	}, s)
}

// Transmission torrent-get without ids has no pagination or implicit result limit.
// Keep raw JSON: the shared doRPC decoder discards duplicate-key evidence.
func (c *Client) automaticTVGet(ctx context.Context, hash string) ([]automaticTVNativeTask, error) {
	if c == nil || c.httpClient == nil {
		return nil, ports.ErrUnavailable
	}
	fields := []string{"hashString", "downloadDir", "labels", "status", "totalSize", "haveValid", "error"}
	args := map[string]any{"fields": fields}
	if hash != "" {
		args["fields"] = append(fields, "files", "fileStats")
		args["ids"] = []string{hash}
	}
	body, err := json.Marshal(rpcRequest{Method: "torrent-get", Arguments: args, Tag: 1})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
		if err != nil {
			return nil, automaticTVReadError(ctx, err)
		}
		req.GetBody = nil
		req.Header.Set("Content-Type", "application/json")
		if c.username != "" || c.password != "" {
			req.SetBasicAuth(c.username, c.password)
		}
		c.mu.Lock()
		sid := c.sessionID
		c.mu.Unlock()
		if sid != "" {
			req.Header.Set("X-Transmission-Session-Id", sid)
		}
		safe := *c.httpClient
		safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := safe.Do(req)
		if err != nil {
			return nil, automaticTVReadError(ctx, err)
		}
		if resp.StatusCode == http.StatusConflict {
			sid = resp.Header.Get("X-Transmission-Session-Id")
			_ = resp.Body.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if sid == "" {
				return nil, ports.ErrAuthRequired
			}
			c.mu.Lock()
			c.sessionID = sid
			c.mu.Unlock()
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		_ = resp.Body.Close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, ports.ErrAuthRequired
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, ports.NewRateLimitError(0, "download RPC rate limited")
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.StatusCode == http.StatusPartialContent || resp.Header.Get("Content-Range") != "" || readErr != nil {
			return nil, automaticTVReadError(ctx, readErr)
		}
		if len(data) > 1<<20 || !utf8.Valid(data) || automaticTVJSON(data) != nil {
			return nil, ports.ErrUnavailable
		}
		var response struct {
			Result    *string `json:"result"`
			Arguments *struct {
				Torrents *[]automaticTVNativeTask `json:"torrents"`
			} `json:"arguments"`
		}
		if json.Unmarshal(data, &response) != nil || response.Result == nil || *response.Result != "success" || response.Arguments == nil || response.Arguments.Torrents == nil {
			return nil, ports.ErrUnavailable
		}
		return *response.Arguments.Torrents, nil
	}
	return nil, ports.ErrUnavailable
}

func automaticTVReadError(ctx context.Context, err error) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}

// Validate all objects, including unknown extension fields, before typed decoding.
func automaticTVJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := automaticTVJSONValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ports.ErrUnavailable
	}
	return nil
}

func automaticTVJSONValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ports.ErrUnavailable
	}
	token, err := d.Token()
	if err != nil {
		return ports.ErrUnavailable
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return ports.ErrUnavailable
			}
			name, ok := key.(string)
			if !ok || seen[name] || !automaticTVJSONKey(name) {
				return ports.ErrUnavailable
			}
			seen[name] = true
			if err := automaticTVJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := automaticTVJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ports.ErrUnavailable
	}
	_, err = d.Token()
	return err
}

func automaticTVJSONKey(name string) bool {
	// encoding/json otherwise accepts case-insensitive aliases for required fields.
	for _, field := range [...]string{"result", "arguments", "torrents", "hashString", "downloadDir", "labels", "status", "totalSize", "haveValid", "error", "files", "fileStats", "name", "length", "bytesCompleted", "wanted"} {
		if strings.EqualFold(name, field) && name != field {
			return false
		}
	}
	return true
}
