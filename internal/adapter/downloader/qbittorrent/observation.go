package qbittorrent

import (
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"math"
	"math/big"
	"strconv"
	"strings"
)

func validTorrentHash(hash string) bool {
	if len(hash) != 40 && len(hash) != 64 {
		return false
	}
	_, e := hex.DecodeString(hash)
	return e == nil
}
func observationProgress(raw json.RawMessage) (float32, error) {
	number, e := exactObservationProgress(raw)
	if e != nil {
		return 0, e
	}
	result := float32(number)
	if result == 1 && number < 1 {
		result = math.Nextafter32(1, 0)
	}
	return result, nil
}
func exactObservationProgress(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 || len(raw) > 64 {
		return 0, ports.ErrBadResponse
	}
	number, e := strconv.ParseFloat(string(raw), 64)
	if e != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > 1 {
		return 0, ports.ErrBadResponse
	}
	// A negative decimal must not become valid zero through float underflow.
	if number == 0 && raw[0] == '-' {
		for _, digit := range raw[1:] {
			if digit == 'e' || digit == 'E' {
				break
			}
			if digit >= '1' && digit <= '9' {
				return 0, ports.ErrBadResponse
			}
		}
	}
	result := number
	if result == 1 {
		exact, ok := new(big.Rat).SetString(string(raw))
		if !ok {
			return 0, ports.ErrBadResponse
		}
		comparison := exact.Cmp(big.NewRat(1, 1))
		if comparison > 0 {
			return 0, ports.ErrBadResponse
		}
		if comparison < 0 {
			result = math.Nextafter(1, 0)
		}
	}
	return result, nil
}
func decodeTorrentObservations(body []byte) ([]*ports.TorrentInfo, error) {
	var rows []struct {
		Hash         *string         `json:"hash"`
		SavePath     *string         `json:"save_path"`
		Progress     json.RawMessage `json:"progress"`
		Size         *int64          `json:"size"`
		Downloaded   *int64          `json:"downloaded"`
		State        *string         `json:"state"`
		Name         string          `json:"name"`
		ContentPath  string          `json:"content_path"`
		Tags         string          `json:"tags"`
		DownloadRate json.RawMessage `json:"dlspeed"`
	}
	if strings.TrimSpace(string(body)) == "null" || json.Unmarshal(body, &rows) != nil {
		return nil, ports.ErrBadResponse
	}
	seen := make(map[string]bool, len(rows))
	result := make([]*ports.TorrentInfo, 0, len(rows))
	for _, row := range rows {
		if row.Hash == nil || !validTorrentHash(*row.Hash) || row.SavePath == nil || *row.SavePath == "" || strings.ContainsAny(*row.SavePath, "\x00\r\n") || row.Size == nil || *row.Size < 0 || row.Downloaded == nil || *row.Downloaded < 0 || row.State == nil || *row.State == "" || strings.ContainsAny(*row.State, "\x00\r\n") {
			return nil, ports.ErrBadResponse
		}
		hash := strings.ToLower(*row.Hash)
		if seen[hash] {
			return nil, ports.ErrBadResponse
		}
		seen[hash] = true
		progress, e := observationProgress(row.Progress)
		if e != nil {
			return nil, e
		}
		result = append(result, &ports.TorrentInfo{InfoHash: hash, SavePath: *row.SavePath, Progress: progress, TotalBytes: *row.Size, DownloadedBytes: *row.Downloaded, State: *row.State, Name: row.Name, ContentPath: row.ContentPath, Tags: row.Tags, DownloadRateBytesPerSecond: nativeDownloadRate(row.DownloadRate)})
	}
	return result, nil
}
