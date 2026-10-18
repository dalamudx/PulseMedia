package torznab

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// searchPageItems validates only the requested window, never whole-search completeness.
// Legacy first pages may omit response metadata; later pages must prove their offset.
func searchPageItems(body []byte, offset, limit int32) ([]rssItem, *ports.IndexerPage, error) {
	var rss rssResponse
	decoder := xml.NewDecoder(bytes.NewReader(body))
	if e := decoder.Decode(&rss); e != nil {
		return nil, nil, ports.ErrBadResponse
	}
	for {
		token, e := decoder.Token()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, nil, ports.ErrBadResponse
		}
		switch v := token.(type) {
		case xml.Comment, xml.ProcInst:
		case xml.CharData:
			if strings.TrimSpace(string(v)) != "" {
				return nil, nil, ports.ErrBadResponse
			}
		default:
			return nil, nil, ports.ErrBadResponse
		}
	}
	if len(rss.Channels) != 1 {
		return nil, nil, ports.ErrBadResponse
	}
	channel := rss.Channels[0]
	proof := &ports.IndexerPage{Offset: offset, Limit: limit}
	count := int64(len(channel.Items))
	if limit > 0 && count > int64(limit) {
		return nil, nil, ports.ErrBadResponse
	}
	if len(channel.Responses) == 0 {
		if offset != 0 {
			return nil, nil, ports.ErrBadResponse
		}
		return channel.Items, proof, nil
	}
	if len(channel.Responses) != 1 {
		return nil, nil, ports.ErrBadResponse
	}
	response := channel.Responses[0]
	start, e := pageNumber(response.Offset)
	if e != nil || start != int64(offset) {
		return nil, nil, ports.ErrBadResponse
	}
	proof.OffsetVerified = true
	if response.Total != nil {
		total, e := pageNumber(*response.Total)
		if e != nil {
			return nil, nil, ports.ErrBadResponse
		}
		proof.Total = &total
		if (count > 0 && (start >= total || count > total-start)) || (count == 0 && start < total) {
			return nil, nil, ports.ErrBadResponse
		}
	}
	return channel.Items, proof, nil
}
func pageNumber(value string) (int64, error) {
	if value == "" {
		return 0, ports.ErrBadResponse
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, ports.ErrBadResponse
		}
	}
	n, e := strconv.ParseInt(value, 10, 64)
	if e != nil {
		return 0, ports.ErrBadResponse
	}
	return n, nil
}
