package fanart

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"path"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// FetchArtwork re-encodes bounded images, preserving PNG alpha and removing metadata.
// It never sends the API credential or cookies to the asset service.
func (c *Client) FetchArtwork(ctx context.Context, reference string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.httpClient == nil || !validImagePath(reference) {
		return nil, ports.ErrRejected
	}
	data, err := c.get(ctx, imageOrigin+reference, maxImageBytes)
	if err != nil {
		return nil, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 16_000_000 {
		return nil, ports.ErrBadResponse
	}
	expected := "jpeg"
	if path.Ext(reference) == ".png" {
		expected = "png"
	}
	if format != expected {
		return nil, ports.ErrBadResponse
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if format == "png" {
		err = png.Encode(&output, decoded)
	} else {
		err = jpeg.Encode(&output, decoded, &jpeg.Options{Quality: 90})
	}
	if err != nil || output.Len() > maxImageBytes {
		return nil, ports.ErrBadResponse
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
