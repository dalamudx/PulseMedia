package torznab_test

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"time"
)

const m4ArchiveSourceName = "PulseMedia M4 lawful Internet Archive"
const m4ArchiveItem = "BigBuckBunny_328"

// Provisioning is separately opt-in, restricted to one named public definition,
// and never disables validation or changes existing source configuration.
func TestM4RealProwlarrArchive(t *testing.T) {
	if os.Getenv("PULSE_M4_PROWLARR_REAL_SOURCE") != "1" {
		t.Skip("explicit real Internet Archive acceptance required")
	}
	p := openM4Prowlarr(t)
	req, err := http.NewRequestWithContext(p.ctx, http.MethodGet, "https://archive.org/metadata/"+m4ArchiveItem, nil)
	if err != nil {
		t.Fatal("public metadata request invalid")
	}
	var phase atomic.Value
	phase.Store("request_created")
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { phase.Store("dns_start") },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			if info.Err != nil {
				phase.Store("dns_failed")
			} else {
				phase.Store("dns_complete")
			}
		},
		ConnectStart: func(string, string) { phase.Store("connect_start") },
		ConnectDone: func(_, _ string, err error) {
			if err != nil {
				phase.Store("connect_failed")
			} else {
				phase.Store("connect_complete")
			}
		},
		TLSHandshakeStart:    func() { phase.Store("tls_handshake_start") },
		GotConn:              func(httptrace.GotConnInfo) { phase.Store("connection_ready") },
		WroteRequest:         func(httptrace.WroteRequestInfo) { phase.Store("request_written") },
		GotFirstResponseByte: func() { phase.Store("response_started") },
	}))
	started := time.Now()
	response, err := p.client.Do(req)
	if err != nil {
		var networkError net.Error
		timeout := errors.As(err, &networkError) && networkError.Timeout()
		t.Fatalf("public archive metadata unavailable phase=%s timeout=%t elapsed_ms=%d (request URL withheld)", phase.Load(), timeout, time.Since(started).Milliseconds())
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("public archive metadata HTTP %d retry_after_present=%t phase=%s elapsed_ms=%d", response.StatusCode, response.Header.Get("Retry-After") != "", phase.Load(), time.Since(started).Milliseconds())
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		t.Fatal("public metadata exceeds bounded contract")
	}
	var metadata struct {
		Metadata struct {
			Identifier string `json:"identifier"`
			Title      string `json:"title"`
			LicenseURL string `json:"licenseurl"`
		} `json:"metadata"`
		Files []struct {
			Name string `json:"name"`
			Size string `json:"size"`
			BTIH string `json:"btih"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &metadata) != nil {
		t.Fatal("invalid archive metadata")
	}
	t.Logf("M4_PUBLIC_METADATA item=%s title=%q license=%q files=%d", m4ArchiveItem, metadata.Metadata.Title, metadata.Metadata.LicenseURL, len(metadata.Files))
	if metadata.Metadata.Identifier != m4ArchiveItem || metadata.Metadata.Title != "Big Buck Bunny" || metadata.Metadata.LicenseURL != "http://creativecommons.org/licenses/by/3.0/us/" {
		t.Fatal("fixed public sample identity or CC BY 3.0 license not established")
	}
	var configured []map[string]json.RawMessage
	if json.Unmarshal(p.read(t, "/api/v1/indexer"), &configured) != nil {
		t.Fatal("invalid configured sources")
	}
	sourceID := 0
	for _, source := range configured {
		var name string
		_ = json.Unmarshal(source["name"], &name)
		if name != m4ArchiveSourceName {
			continue
		}
		var definition string
		var enabled bool
		_ = json.Unmarshal(source["definitionName"], &definition)
		_ = json.Unmarshal(source["enable"], &enabled)
		if definition != "internetarchive" || !enabled || sourceID != 0 {
			t.Fatal("owned source identity or state mismatch")
		}
		if json.Unmarshal(source["id"], &sourceID) != nil || sourceID <= 0 {
			t.Fatal("invalid owned source ID")
		}
	}
	if sourceID == 0 {
		if os.Getenv("PULSE_M4_PROWLARR_PROVISION") != "1" {
			t.Fatal("named real source absent; explicit provisioning required")
		}
		var definitions []map[string]json.RawMessage
		if json.Unmarshal(p.read(t, "/api/v1/indexer/schema"), &definitions) != nil {
			t.Fatal("invalid source definitions")
		}
		var chosen map[string]json.RawMessage
		for _, d := range definitions {
			var name, privacy string
			_ = json.Unmarshal(d["definitionName"], &name)
			_ = json.Unmarshal(d["privacy"], &privacy)
			if name == "internetarchive" && privacy == "public" {
				chosen = d
				break
			}
		}
		if chosen == nil {
			t.Fatal("built-in public Internet Archive definition unavailable")
		}
		var fields []map[string]json.RawMessage
		if json.Unmarshal(chosen["fields"], &fields) != nil {
			t.Fatal("invalid public source fields")
		}
		for _, f := range fields {
			var name string
			_ = json.Unmarshal(f["name"], &name)
			switch name {
			case "baseUrl":
				f["value"] = json.RawMessage(`"https://archive.org/"`)
			case "titleOnly":
				f["value"] = json.RawMessage(`false`)
			case "noMagnet":
				f["value"] = json.RawMessage(`true`)
			}
		}
		chosen["fields"], err = json.Marshal(fields)
		if err != nil {
			t.Fatal("cannot encode source fields")
		}
		chosen["name"], _ = json.Marshal(m4ArchiveSourceName)
		chosen["enable"] = json.RawMessage(`true`)
		chosen["priority"] = json.RawMessage(`25`)
		var profiles []struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(p.read(t, "/api/v1/appprofile"), &profiles) != nil || len(profiles) == 0 || profiles[0].ID <= 0 {
			t.Fatal("real application profile unavailable")
		}
		chosen["appProfileId"], _ = json.Marshal(profiles[0].ID)
		delete(chosen, "id")
		body, e := json.Marshal(chosen)
		if e != nil {
			t.Fatal("cannot encode owned source")
		}
		status, result := p.request(t, http.MethodPost, "/api/v1/indexer", body)
		if status != http.StatusCreated {
			var issues []struct {
				PropertyName string `json:"propertyName"`
				ErrorMessage string `json:"errorMessage"`
			}
			if json.Unmarshal(result, &issues) == nil {
				for _, issue := range issues {
					message := strings.ReplaceAll(issue.ErrorMessage, p.key, "[credential]")
					message = regexp.MustCompile(`https?://[^\s<>"']+`).ReplaceAllString(message, "[URL]")
					if len(message) > 500 {
						message = message[:500]
					}
					t.Logf("M4_SOURCE_VALIDATION property=%q reason=%q", issue.PropertyName, message)
				}
			}
			t.Fatalf("real Internet Archive provisioning HTTP %d; validation not bypassed", status)
		}
		var created struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(result, &created) != nil || created.ID <= 0 {
			t.Fatal("invalid created source identity")
		}
		sourceID = created.ID
		t.Logf("M4_REAL_SOURCE_CREATED id=%d definition=internetarchive", sourceID)
	}
	client := torznab.NewClient("http://127.0.0.1:19696/"+strconv.Itoa(sourceID), p.key, p.client)
	caps, err := client.Capabilities(p.ctx)
	if err != nil {
		t.Fatalf("production real capabilities: %v", err)
	}
	supportedQ := false
	for _, param := range caps.MovieSearch.SupportedParams {
		if param == "q" {
			supportedQ = true
		}
	}
	if !caps.MovieSearch.Available || !supportedQ {
		t.Fatal("real source lacks production movie query capability")
	}
	t.Logf("M4_REAL_CAPABILITIES_PASSED source=%d", sourceID)
	page, err := client.SearchPage(p.ctx, "identifier:"+m4ArchiveItem, []int32{2000}, 0, 100)
	if err != nil {
		t.Fatalf("production real search: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Title != "Big Buck Bunny" {
		t.Fatalf("fixed lawful result not uniquely established: count=%d", len(page.Items))
	}
	// GUIDs are opaque provider identities. The fetched original-info hash and
	// file inventory below bind the result to the independently licensed item.
	if page.Items[0].GUID == "" {
		t.Fatal("real source result has no provider identity")
	}
	t.Logf("M4_REAL_SEARCH_PASSED source=%d exact_items=1", sourceID)
	raw, err := client.FetchTorrent(p.ctx, page.Items[0].DownloadURL)
	if err != nil {
		t.Fatalf("production real fetch: %v", err)
	}
	proof, err := (torrent.Inspector{}).InspectTorrent(raw)
	if err != nil {
		t.Fatalf("production bounded metainfo verification: %v", err)
	}
	if len(proof.Files) == 0 || proof.TotalBytes <= 0 {
		t.Fatal("real metainfo lacks file evidence")
	}
	matchedHash := false
	for _, f := range metadata.Files {
		if f.Name == m4ArchiveItem+"_archive.torrent" {
			size, e := strconv.ParseInt(f.Size, 10, 64)
			matchedHash = e == nil && size == int64(len(raw)) && strings.EqualFold(f.BTIH, proof.InfoHash)
		}
	}
	if !matchedHash {
		t.Fatal("metainfo size or infohash differs from independent archive torrent metadata")
	}
	known := make(map[string]int64, len(metadata.Files))
	for _, f := range metadata.Files {
		if size, e := strconv.ParseInt(f.Size, 10, 64); e == nil {
			known[m4ArchiveItem+"/"+f.Name] = size
		}
	}
	for _, f := range proof.Files {
		if size, ok := known[f.Path]; !ok || size != f.Size {
			t.Fatal("torrent file differs from independent public archive inventory")
		}
	}
	t.Logf("M4_REAL_TORZNAB_ACCEPTED source=%d item=%s license=CC-BY-3.0-US metainfo_bytes=%d infohash=%s files=%d content_bytes=%d", sourceID, m4ArchiveItem, len(raw), proof.InfoHash, len(proof.Files), proof.TotalBytes)
}
