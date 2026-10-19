package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// AutomaticTVObserver binds only native reads to this client configuration.
func (c *Client) AutomaticTVObserver() ports.AutomaticTVReadClient {
	return &automaticTVReader{client: c}
}

// Do not embed Client: its mutation and export methods are not this capability.
type automaticTVReader struct{ client *Client }

var _ ports.AutomaticTVReadClient = (*automaticTVReader)(nil)

const automaticTVResponseLimit int64 = 1 << 20 // Preserve the existing stricter file-response cap.

func (r *automaticTVReader) ListAutomaticTVTasks(ctx context.Context, tag string) ([]ports.AutomaticTVRemoteTask, error) {
	if !automaticTVOperationTag(tag) {
		return nil, ports.ErrBadResponse
	}
	tasks, err := r.tasks(ctx, url.Values{"tag": {tag}})
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		found := false
		for _, label := range task.Tags {
			found = found || label == tag
		}
		// A server ignoring the filter cannot establish a complete lookup.
		if !found {
			return nil, ports.ErrUnavailable
		}
	}
	return tasks, nil
}

func automaticTVOperationTag(tag string) bool {
	const prefix = "pulsemedia:op:"
	if !strings.HasPrefix(tag, prefix) {
		return false
	}
	id, err := uuid.Parse(strings.TrimPrefix(tag, prefix))
	return err == nil && id != uuid.Nil && tag == prefix+id.String()
}

func (r *automaticTVReader) read(ctx context.Context, endpoint string, query url.Values) ([]byte, error) {
	c := r.client
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	u.RawPath = strings.TrimRight(u.EscapedPath(), "/") + "/api/v2/torrents/" + endpoint
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v2/torrents/" + endpoint
	q := u.Query()
	// Configured query parameters must not narrow or change the native lookup.
	for _, key := range []string{"tag", "hash", "hashes", "filter", "limit", "offset", "sort", "reverse", "indexes"} {
		q.Del(key)
	}
	for key, values := range query {
		q[key] = values
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return nil, httpFailure(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if err = c.apiHTTPFailure(response); err != nil {
		if errors.Is(err, ports.ErrBadResponse) || errors.Is(err, ports.ErrNotFound) {
			return nil, ports.ErrUnavailable
		}
		return nil, err
	}
	body, err := readHTTPBody(ctx, response.Body, automaticTVResponseLimit)
	if errors.Is(err, ports.ErrBadResponse) {
		return nil, ports.ErrUnavailable
	}
	return body, err
}

func (r *automaticTVReader) tasks(ctx context.Context, query url.Values) ([]ports.AutomaticTVRemoteTask, error) {
	body, err := r.read(ctx, "info", query)
	if err != nil {
		return nil, err
	}
	if !automaticTVUniqueJSON(body) {
		return nil, ports.ErrUnavailable
	}
	var rows []struct {
		Hash         *string         `json:"hash"`
		SavePath     *string         `json:"save_path"`
		Tags         *string         `json:"tags"`
		State        *string         `json:"state"`
		Total        *int64          `json:"total_size"`
		Completed    *int64          `json:"completed"`
		Progress     json.RawMessage `json:"progress"`
		Name         json.RawMessage `json:"name"`
		ContentPath  json.RawMessage `json:"content_path"`
		DownloadRate json.RawMessage `json:"dlspeed"`
	}
	if json.Unmarshal(body, &rows) != nil || rows == nil || len(rows) > 64 {
		return nil, ports.ErrUnavailable
	}
	out := make([]ports.AutomaticTVRemoteTask, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.Hash == nil || !automaticTVHash(*row.Hash) || row.SavePath == nil || !automaticTVAbsolutePath(*row.SavePath) || row.Tags == nil || !automaticTVText(*row.Tags) || row.State == nil || row.Total == nil || row.Completed == nil || *row.Total < 0 || *row.Completed < 0 || *row.Completed > *row.Total {
			return nil, ports.ErrUnavailable
		}
		completeState, known := automaticTVState(*row.State)
		if !known || (row.Progress != nil && !automaticTVProgressValid(row.Progress)) {
			return nil, ports.ErrUnavailable
		}
		if row.Name != nil {
			var name string
			if json.Unmarshal(row.Name, &name) != nil || bytes.Equal(row.Name, []byte("null")) || name == "" || !automaticTVText(name) {
				return nil, ports.ErrUnavailable
			}
		}
		content := ""
		if row.ContentPath != nil && (json.Unmarshal(row.ContentPath, &content) != nil || !automaticTVAbsolutePath(content)) {
			return nil, ports.ErrUnavailable
		}
		hash := strings.ToLower(*row.Hash)
		if seen[hash] {
			return nil, ports.ErrUnavailable
		}
		seen[hash] = true
		tags := make([]string, 0)
		for _, tag := range strings.Split(*row.Tags, ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				tags = append(tags, tag)
			}
		}
		out = append(out, ports.AutomaticTVRemoteTask{InfoHash: hash, SavePath: *row.SavePath, ContentPath: content, State: *row.State, Tags: tags, TotalBytes: *row.Total, VerifiedBytes: *row.Completed, Complete: completeState && *row.Total == *row.Completed, DownloadRateBytesPerSecond: nativeDownloadRate(row.DownloadRate)})
	}
	return out, nil
}

func automaticTVState(state string) (complete, known bool) {
	switch state {
	case "uploading", "stalledUP", "pausedUP", "stoppedUP", "queuedUP", "forcedUP":
		return true, true
	case "error", "missingFiles", "checkingUP", "allocating", "downloading", "metaDL", "forcedMetaDL", "pausedDL", "stoppedDL", "queuedDL", "stalledDL", "checkingDL", "forcedDL", "checkingResumeData", "moving":
		return false, true
	default:
		return false, false
	}
}

func automaticTVHash(hash string) bool { return len(hash) == 40 && validTorrentHash(hash) }

func automaticTVProgressValid(raw json.RawMessage) bool {
	_, err := exactObservationProgress(raw)
	return err == nil
}

func (r *automaticTVReader) task(ctx context.Context, hash string) (*ports.AutomaticTVRemoteTask, error) {
	tasks, err := r.tasks(ctx, url.Values{"hashes": {hash}})
	if err != nil {
		return nil, err
	}
	if len(tasks) != 1 || tasks[0].InfoHash != hash || !tasks[0].Complete {
		return nil, ports.ErrUnavailable
	}
	return &tasks[0], nil
}

func (r *automaticTVReader) GetAutomaticTVFiles(ctx context.Context, hash string) ([]ports.AutomaticTVRemoteFile, error) {
	if !automaticTVHash(hash) {
		return nil, ports.ErrBadResponse
	}
	hash = strings.ToLower(hash)
	before, err := r.task(ctx, hash)
	if err != nil {
		return nil, err
	}
	body, err := r.read(ctx, "files", url.Values{"hash": {hash}})
	if err != nil {
		return nil, err
	}
	if !automaticTVUniqueJSON(body) {
		return nil, ports.ErrUnavailable
	}
	var rows []struct {
		Index    *int            `json:"index"`
		Name     *string         `json:"name"`
		Size     *int64          `json:"size"`
		Priority *int64          `json:"priority"`
		Progress json.RawMessage `json:"progress"`
	}
	if json.Unmarshal(body, &rows) != nil || len(rows) == 0 || len(rows) > 4096 {
		return nil, ports.ErrUnavailable
	}
	out := make([]ports.AutomaticTVRemoteFile, 0, len(rows))
	ids := make([]bool, len(rows))
	names := make(map[string]bool, len(rows))
	var total int64
	for _, row := range rows {
		if row.Index == nil || *row.Index < 0 || *row.Index >= len(rows) || ids[*row.Index] || row.Name == nil || row.Size == nil || *row.Size < 0 || row.Priority == nil || *row.Priority < 0 {
			return nil, ports.ErrUnavailable
		}
		name := *row.Name
		key := strings.ToLower(name)
		if name == "" || len(name) > 1024 || !automaticTVText(name) || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:") || names[key] || *row.Size > math.MaxInt64-total {
			return nil, ports.ErrUnavailable
		}
		progress, err := exactObservationProgress(row.Progress)
		if err != nil || progress != 1 {
			return nil, ports.ErrUnavailable
		}
		ids[*row.Index], names[key] = true, true
		total += *row.Size
		out = append(out, ports.AutomaticTVRemoteFile{Index: *row.Index, Path: name, Size: *row.Size, Wanted: *row.Priority > 0})
	}
	if total != before.TotalBytes {
		return nil, ports.ErrUnavailable
	}
	after, err := r.task(ctx, hash)
	if err != nil {
		return nil, err
	}
	if after.SavePath != before.SavePath || after.ContentPath != before.ContentPath || after.TotalBytes != before.TotalBytes {
		return nil, ports.ErrUnavailable
	}
	// qB has no exact per-file byte counter. The stable verified ALL-file total,
	// equal inventory sum and exact per-file progress jointly prove these bytes.
	for i := range out {
		out[i].CompletedBytes = out[i].Size
	}
	return out, nil
}

func automaticTVText(s string) bool {
	if !utf8.ValidString(s) || len(s) > 4096 {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func automaticTVAbsolutePath(s string) bool {
	if s == "" || !automaticTVText(s) {
		return false
	}
	p := s
	windows := len(p) >= 3 && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
	unc := strings.HasPrefix(p, `\\`)
	if windows || unc {
		if strings.Contains(p, `\`) && strings.Contains(p, "/") {
			return false
		}
		p = strings.ReplaceAll(p, `\`, "/")
		if windows {
			p = p[2:]
		} else {
			p = p[1:]
			if len(strings.Split(strings.Trim(p, "/"), "/")) < 2 {
				return false
			}
		}
		for _, part := range strings.Split(p, "/") {
			if strings.ContainsAny(part, `:<>"|?*`) || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
				return false
			}
		}
	} else if strings.ContainsAny(p, `\:`) {
		return false
	}
	return path.IsAbs(p) && !strings.Contains(p, "//") && (path.Clean(p) == p || path.Clean(p)+"/" == p)
}

// Check every object, including unused nested fields; encoding/json otherwise
// silently accepts duplicate evidence. Bound recursion independently of body size.
func automaticTVUniqueJSON(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if !automaticTVJSONValue(d, 0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func automaticTVJSONValue(d *json.Decoder, depth int) bool {
	if depth > 64 {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || !automaticTVText(key) {
				return false
			}
			for _, c := range key {
				if c > 127 {
					return false
				}
			}
			key = strings.ToLower(key)
			if seen[key] || !automaticTVJSONValue(d, depth+1) {
				return false
			}
			seen[key] = true
		}
		end, err := d.Token()
		return err == nil && end == json.Delim('}')
	case json.Delim('['):
		for d.More() {
			if !automaticTVJSONValue(d, depth+1) {
				return false
			}
		}
		end, err := d.Token()
		return err == nil && end == json.Delim(']')
	default:
		_, delimiter := token.(json.Delim)
		return !delimiter
	}
}
