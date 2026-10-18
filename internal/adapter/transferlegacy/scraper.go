package transfer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ScrapeAndSave writes NFO and downloads poster/fanart into the media folder.
func ScrapeAndSave(ctx context.Context, targetDir, nfoFilename string, nfoData []byte, posterURL, fanartURL string, client *http.Client) error {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create target dir: %w", err)
	}

	// 1. Write NFO file
	nfoPath := filepath.Join(targetDir, nfoFilename)
	if len(nfoData) > 0 {
		_ = os.WriteFile(nfoPath, nfoData, 0644)
	}

	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	// 2. Download Poster (graceful degradation on network timeout)
	if posterURL != "" {
		posterPath := filepath.Join(targetDir, "poster.jpg")
		_ = downloadImage(ctx, client, posterURL, posterPath)
	}

	// 3. Download Fanart (graceful degradation on network timeout)
	if fanartURL != "" {
		fanartPath := filepath.Join(targetDir, "fanart.jpg")
		_ = downloadImage(ctx, client, fanartURL, fanartPath)
	}

	return nil
}

func downloadImage(ctx context.Context, client *http.Client, imageURL, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	_, err = io.Copy(out, resp.Body)
	return err
}
