package search

import (
	"bytes"
	"math"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	maxIndexerConfigurationURLBytes        = 8192
	maxIndexerConfigurationCredentialBytes = 8192
	maxIndexerConfigurationCategories      = 100
)

// PrepareIndexerConfiguration validates locally and encrypts the exact input
// bytes using the injected system envelope. It performs no network or storage
// I/O. Empty credentials are encrypted as empty bytes, including public feeds.
// Invalid inputs return ErrRejected; missing or failed encryption returns
// ErrUnavailable. Errors and failed results never include secret material.
func PrepareIndexerConfiguration(input ports.IndexerConfigurationInput, secrets ports.SecretProtector) (ports.PreparedIndexerConfiguration, error) {
	var zero ports.PreparedIndexerConfiguration
	switch input.Protocol {
	case ports.IndexerProtocolTorznab, ports.IndexerProtocolNexusPHP, ports.IndexerProtocolRSS:
	default:
		return zero, ports.ErrRejected
	}
	if len(input.URL) == 0 || len(input.URL) > maxIndexerConfigurationURLBytes ||
		len(input.Credential) > maxIndexerConfigurationCredentialBytes ||
		len(input.Categories) > maxIndexerConfigurationCategories ||
		!utf8.ValidString(input.URL) || !utf8.ValidString(input.Credential) ||
		strings.ContainsFunc(input.URL, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) ||
		strings.ContainsFunc(input.Credential, unicode.IsControl) || strings.Contains(input.URL, "#") {
		return zero, ports.ErrRejected
	}
	// Validate escapes in the entire URL, including RawQuery, which url.Parse
	// deliberately leaves unchecked. Encoded controls are also rejected.
	decoded, err := url.PathUnescape(input.URL)
	if err != nil || strings.ContainsFunc(decoded, unicode.IsControl) {
		return zero, ports.ErrRejected
	}
	u, err := url.Parse(input.URL)
	if err != nil || u.Opaque != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") || !validIndexerConfigurationHost(u) {
		return zero, ports.ErrRejected
	}
	seen := make(map[int]struct{}, len(input.Categories))
	for _, category := range input.Categories {
		if category < 1 || category > math.MaxInt32 {
			return zero, ports.ErrRejected
		}
		if _, duplicate := seen[category]; duplicate {
			return zero, ports.ErrRejected
		}
		seen[category] = struct{}{}
	}
	if secrets == nil {
		return zero, ports.ErrUnavailable
	}
	urlCiphertext, err := secrets.EncryptSecret([]byte(input.URL))
	if err != nil || len(urlCiphertext) == 0 {
		return zero, ports.ErrUnavailable
	}
	// A codec may reuse its output buffer on the next call.
	urlCiphertext = bytes.Clone(urlCiphertext)
	credentialCiphertext, err := secrets.EncryptSecret([]byte(input.Credential))
	if err != nil || len(credentialCiphertext) == 0 {
		return zero, ports.ErrUnavailable
	}
	return ports.PreparedIndexerConfiguration{
		Protocol:             input.Protocol,
		DisplayURL:           (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(),
		URLCiphertext:        urlCiphertext,
		CredentialCiphertext: bytes.Clone(credentialCiphertext),
		Categories:           slices.Clone(input.Categories),
	}, nil
}

func validIndexerConfigurationHost(u *url.URL) bool {
	host := u.Hostname()
	if host == "" || strings.HasSuffix(u.Host, ":") {
		return false
	}
	if strings.HasPrefix(u.Host, "[") {
		addr, err := netip.ParseAddr(host)
		if err != nil || !addr.Is6() {
			return false
		}
	} else {
		if strings.Contains(host, ":") {
			return false
		}
		for _, r := range host {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
				return false
			}
		}
		if strings.Trim(host, ".") == "" {
			return false
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}
