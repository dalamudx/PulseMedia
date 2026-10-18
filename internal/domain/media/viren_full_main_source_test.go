package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestVirenFullMainPendingSourceRemainsVerbatim(t *testing.T) {
	root := "../../../testdata/releases/"
	raw, err := os.ReadFile(root + "upstream-mit/viren-full-main-original.test.ts")
	if err != nil {
		t.Fatal(err)
	}
	license, err := os.ReadFile(root + "review-inputs/viren-title-original-assertions.LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		data []byte
		hash string
	}{{raw, "4b25201f0f2e4a04929ad154b91252f8bc4c9a24a36aac322987e12e3d79f5cd"}, {license, "5c44be7aac21777dcc84933675ee7f0b08c736aeaed6c78aedd0e3d7a6856d3a"}} {
		h := sha256.Sum256(c.data)
		if hex.EncodeToString(h[:]) != c.hash {
			t.Fatal("verbatim source or full copyright/license changed")
		}
	}
	if !bytes.Contains(raw, []byte("intRange(1, 1071)")) || !bytes.Contains(raw, []byte("seasons: [1, 2, 3, 4, 5, 6]")) {
		t.Fatal("original range expression or explicit season declaration lost")
	}
	if !bytes.Contains(license, []byte("Copyright (c) 2025 Viren070")) || !bytes.Contains(license, []byte("MIT License")) {
		t.Fatal("missing original attribution")
	}
	data, err := os.ReadFile(root + "review-inputs/viren-full-main-original.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Source     string `json:"source_id"`
		Status     string `json:"review_status"`
		Count      int    `json:"reviewed_entries"`
		Golden     *bool  `json:"formal_golden_acceptance"`
		Authority  *bool  `json:"coverage_authority"`
		Provenance struct {
			Repository  string `json:"repository"`
			Commit      string `json:"commit"`
			Path        string `json:"path"`
			Hash        string `json:"source_sha256"`
			LicenseHash string `json:"license_sha256"`
		} `json:"provenance"`
	}
	if err = json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Source != "viren-full-main-original" || m.Status != "unreviewed_verbatim_source" || m.Count != 0 || m.Golden == nil || *m.Golden || m.Authority == nil || *m.Authority {
		t.Fatal("raw pending source promoted to gold or execution coverage")
	}
	if m.Provenance.Repository != "https://github.com/Viren070/parse-torrent-title" || m.Provenance.Commit != "46bc6fef150649410d8d307a08e97ba3a3559eb6" || m.Provenance.Path != "src/__tests__/ptt.test.ts" || m.Provenance.Hash != "4b25201f0f2e4a04929ad154b91252f8bc4c9a24a36aac322987e12e3d79f5cd" || m.Provenance.LicenseHash != "5c44be7aac21777dcc84933675ee7f0b08c736aeaed6c78aedd0e3d7a6856d3a" {
		t.Fatal("fixed source provenance drifted")
	}
}
