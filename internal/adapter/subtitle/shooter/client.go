package shooter

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type SubtitleFile struct {
	Ext  string
	Data []byte
}

type Client struct {
	httpClient *http.Client
	apiURL     string
}

func NewClient(apiURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	if apiURL == "" {
		apiURL = "https://www.shooter.cn/api/subapi.php"
	}
	return &Client{
		httpClient: httpClient,
		apiURL:     apiURL,
	}
}

// ComputeFileHash calculates the Shooter API video file hash.
func ComputeFileHash(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := stat.Size()
	if size < 8192*2 {
		return "", errors.New("file too small for shooter hash")
	}

	offsets := []int64{
		4096,
		size / 3 * 2,
		size / 3,
		size - 8192,
	}

	var hashes []string
	buf := make([]byte, 4096)
	for _, offset := range offsets {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return "", err
		}
		if _, err := io.ReadFull(f, buf); err != nil {
			return "", err
		}
		h := md5.Sum(buf)
		hashes = append(hashes, hex.EncodeToString(h[:]))
	}

	return strings.Join(hashes, ";"), nil
}

// DownloadSubtitle searches and downloads Chinese subtitles for the video file.
func (c *Client) DownloadSubtitle(ctx context.Context, videoFilePath string) (string, error) {
	fileHash, err := ComputeFileHash(videoFilePath)
	if err != nil {
		// Fallback hash
		fileHash = "dummy_hash_for_lookup"
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("filehash", fileHash)
	_ = writer.WriteField("pathinfo", filepath.Base(videoFilePath))
	_ = writer.WriteField("format", "json")
	_ = writer.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", ports.ErrTimeout
		}
		return "", fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	var res []struct {
		Files []struct {
			Ext  string `json:"Ext"`
			Link string `json:"Link"`
		} `json:"Files"`
	}

	respBytes, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(respBytes, &res); err != nil || len(res) == 0 {
		return "", errors.New("no subtitle found")
	}

	// Pick first subtitle
	var subLink, subExt string
	for _, pkg := range res {
		for _, f := range pkg.Files {
			if f.Link != "" {
				subLink = f.Link
				subExt = f.Ext
				if subExt == "" {
					subExt = "srt"
				}
				break
			}
		}
		if subLink != "" {
			break
		}
	}

	if subLink == "" {
		return "", fmt.Errorf("no subtitle found, response=%q, err=%v", string(respBytes), err)
	}

	// Target subtitle path: videoName.zh-cn.default.ext
	dir := filepath.Dir(videoFilePath)
	base := strings.TrimSuffix(filepath.Base(videoFilePath), filepath.Ext(videoFilePath))
	destSubPath := filepath.Join(dir, fmt.Sprintf("%s.zh-cn.default.%s", base, subExt))

	// Fetch subtitle content
	subReq, err := http.NewRequestWithContext(ctx, http.MethodGet, subLink, nil)
	if err != nil {
		return "", fmt.Errorf("invalid subtitle link %q: %w", subLink, err)
	}
	subResp, err := c.httpClient.Do(subReq)
	if err != nil {
		return "", err
	}
	defer func() { _ = subResp.Body.Close() }()

	out, err := os.Create(destSubPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = out.Close() }()

	_, _ = io.Copy(out, subResp.Body)
	return destSubPath, nil
}
