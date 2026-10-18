package torznab

import (
	"context"
	"encoding/xml"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/url"
	"strconv"
	"strings"
)

var _ ports.IndexerCapabilitiesProvider = (*Client)(nil)

type capsSearch struct {
	Available string `xml:"available,attr"`
	Params    string `xml:"supportedParams,attr"`
}
type capsResponse struct {
	XMLName xml.Name `xml:"caps"`
	Limits  *struct {
		Default string `xml:"default,attr"`
		Max     string `xml:"max,attr"`
	} `xml:"limits"`
	Searching *struct {
		Search *capsSearch `xml:"search"`
		Movie  *capsSearch `xml:"movie-search"`
		TV     *capsSearch `xml:"tv-search"`
	} `xml:"searching"`
}

func (c *Client) Capabilities(ctx context.Context) (ports.IndexerCapabilities, error) {
	var out ports.IndexerCapabilities
	params := url.Values{"t": {"caps"}, "apikey": {c.apiKey}}
	requestURL, e := c.apiURL(params)
	if e != nil {
		return out, e
	}
	body, e := c.get(ctx, requestURL, 5<<20)
	if e != nil {
		return out, e
	}
	if e = apiError(body); e != nil {
		return out, e
	}
	var raw capsResponse
	if xml.Unmarshal(body, &raw) != nil || raw.Searching == nil {
		return out, ports.ErrBadResponse
	}
	if raw.Limits != nil {
		def, e1 := strconv.ParseInt(raw.Limits.Default, 10, 32)
		max, e2 := strconv.ParseInt(raw.Limits.Max, 10, 32)
		if e1 != nil || e2 != nil || def <= 0 || max <= 0 || def > max {
			return out, ports.ErrBadResponse
		}
		out.DefaultLimit, out.MaxLimit = int32(def), int32(max)
	}
	if out.Search, e = parseCapability(raw.Searching.Search); e != nil {
		return ports.IndexerCapabilities{}, e
	}
	if out.MovieSearch, e = parseCapability(raw.Searching.Movie); e != nil {
		return ports.IndexerCapabilities{}, e
	}
	if out.TVSearch, e = parseCapability(raw.Searching.TV); e != nil {
		return ports.IndexerCapabilities{}, e
	}
	return out, nil
}
func parseCapability(raw *capsSearch) (ports.IndexerSearchCapability, error) {
	var out ports.IndexerSearchCapability
	if raw == nil {
		return out, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw.Available)) {
	case "yes":
		out.Available = true
	case "no":
	default:
		return out, ports.ErrBadResponse
	}
	seen := map[string]bool{}
	for _, value := range strings.Split(raw.Params, ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !seen[value] {
			out.SupportedParams = append(out.SupportedParams, value)
			seen[value] = true
		}
	}
	return out, nil
}

// Provider error descriptions are untrusted and can contain credentials.
func apiError(body []byte) error {
	var root struct {
		XMLName xml.Name
		Code    string `xml:"code,attr"`
	}
	if xml.Unmarshal(body, &root) != nil {
		return ports.ErrBadResponse
	}
	if root.XMLName.Local != "error" {
		return nil
	}
	switch root.Code {
	case "100", "101", "102":
		return ports.ErrAuthRequired
	default:
		return ports.ErrBadResponse
	}
}
