package postgres

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func endpointConfigurationFamily(f ports.EndpointFamily) bool {
	return f == ports.DownloadClientEndpoint || f == ports.MediaServerEndpoint
}

func endpointConfigurationText(v string, max int) bool {
	return len(v) <= max && utf8.ValidString(v) && !strings.ContainsFunc(v, unicode.IsControl)
}

func validateEndpointConfiguration(f ports.EndpointFamily, in ports.EndpointConfigurationInput) error {
	if !endpointConfigurationFamily(f) || !endpointConfigurationText(in.Name, 256) || strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Name) != in.Name || !endpointConfigurationText(in.Username, 1024) || !endpointConfigurationText(in.Secret, 65536) || !endpointConfigurationText(in.URL, 16384) {
		return ports.ErrInvalidEndpointConfiguration
	}
	if f == ports.DownloadClientEndpoint {
		if in.Protocol != "qbittorrent" && in.Protocol != "transmission" && in.Protocol != "rtorrent" {
			return ports.ErrInvalidEndpointConfiguration
		}
	} else if (in.Protocol != "jellyfin" && in.Protocol != "plex" && in.Protocol != "emby") || in.Username != "" {
		return ports.ErrInvalidEndpointConfiguration
	}
	if len(in.Mapping) > 65536 {
		return ports.ErrInvalidEndpointConfiguration
	}
	if len(in.Mapping) > 0 {
		var mapping map[string]string
		if err := json.Unmarshal(in.Mapping, &mapping); err != nil || mapping == nil || len(mapping) > 256 {
			return ports.ErrInvalidEndpointConfiguration
		}
		// Unmarshal accepts null string values; the input contract requires strings.
		var values map[string]json.RawMessage
		if err := json.Unmarshal(in.Mapping, &values); err != nil {
			return ports.ErrInvalidEndpointConfiguration
		}
		for key, value := range mapping {
			if !endpointConfigurationText(key, 4096) || key == "" || !endpointConfigurationText(value, 4096) || value == "" || bytes.Equal(bytes.TrimSpace(values[key]), []byte("null")) {
				return ports.ErrInvalidEndpointConfiguration
			}
		}
	}
	return nil
}

func endpointConfigurationURL(raw string) bool {
	if raw == "" || !endpointConfigurationText(raw, 16384) || strings.TrimSpace(raw) != raw || endpoint.URLHasMask(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(u.Host, " \\ \t\r\n") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return false
	}
	_, err = url.ParseQuery(u.RawQuery)
	return err == nil
}
