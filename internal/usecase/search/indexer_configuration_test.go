package search_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func configurationCodec() *crypto.SecretProtector {
	return crypto.NewSecretProtector(bytes.Repeat([]byte{0x42}, 32))
}

func TestPrepareIndexerConfigurationRoundTrip(t *testing.T) {
	t.Parallel()
	for _, protocol := range []ports.IndexerProtocol{ports.IndexerProtocolTorznab, ports.IndexerProtocolNexusPHP, ports.IndexerProtocolRSS} {
		for _, credential := range []string{"", "public", "opaque-key", "session=secret; passkey=value", " passkey=secret "} {
			t.Run(string(protocol)+"/"+credential, func(t *testing.T) {
				codec := configurationCodec()
				input := ports.IndexerConfigurationInput{
					Protocol:   protocol,
					URL:        "https://user-secret:userinfo-secret@localhost:8443/arbitrary-path-secret/%2f?unknown=query-secret&x=%2B&x=second&empty=",
					Credential: credential,
					Categories: []int{2000, 5000, 100001},
				}
				prepared, err := search.PrepareIndexerConfiguration(input, codec)
				if err != nil {
					t.Fatal(err)
				}
				if prepared.Protocol != protocol || prepared.DisplayURL != "https://localhost:8443" || !reflect.DeepEqual(prepared.Categories, input.Categories) {
					t.Fatal("prepared public fields differ")
				}
				for _, secret := range []struct {
					ciphertext []byte
					plain      string
				}{{prepared.URLCiphertext, input.URL}, {prepared.CredentialCiphertext, credential}} {
					plain, err := codec.DecryptSecret(secret.ciphertext)
					if err != nil || string(plain) != secret.plain || bytes.Equal(secret.ciphertext, plain) {
						t.Fatal("system envelope failed exact byte round trip")
					}
				}
				input.Categories[0] = 1234
				if prepared.Categories[0] != 2000 {
					t.Fatal("result aliases caller categories")
				}
				prepared.Categories[1] = 4321
				if input.Categories[1] != 5000 {
					t.Fatal("caller aliases result categories")
				}
				for _, dto := range []any{input, prepared} {
					raw, err := json.Marshal(dto)
					if err != nil {
						t.Fatal(err)
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(raw, &fields); err != nil {
						t.Fatal(err)
					}
					for _, field := range []string{"URL", "Credential", "URLCiphertext", "CredentialCiphertext"} {
						if _, exists := fields[field]; exists {
							t.Fatal("JSON exposes secret field")
						}
					}
					if strings.Contains(string(raw), "secret") {
						t.Fatal("JSON exposes secret content")
					}
				}
			})
		}
	}
}

func TestPrepareIndexerConfigurationLocalOrigins(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{"http://localhost", "http://127.0.0.1:1", "https://192.168.1.2:65535", "http://[::1]:8080", "http://[fe80::1%25eth0]:8080", "https://tracker_internal"} {
		t.Run(origin, func(t *testing.T) {
			input := ports.IndexerConfigurationInput{Protocol: ports.IndexerProtocolRSS, URL: origin + "/private?unknown=secret"}
			got, err := search.PrepareIndexerConfiguration(input, configurationCodec())
			if err != nil || got.DisplayURL != origin {
				t.Fatal("explicit local origin rejected or changed")
			}
		})
	}
}

func TestPrepareIndexerConfigurationRejected(t *testing.T) {
	t.Parallel()
	valid := ports.IndexerConfigurationInput{Protocol: ports.IndexerProtocolTorznab, URL: "https://localhost/api", Credential: "private-secret"}
	cases := []struct {
		name   string
		change func(*ports.IndexerConfigurationInput)
	}{
		{"missing protocol", func(i *ports.IndexerConfigurationInput) { i.Protocol = "" }},
		{"unknown protocol", func(i *ports.IndexerConfigurationInput) { i.Protocol = "private-secret" }},
		{"case sensitive protocol", func(i *ports.IndexerConfigurationInput) { i.Protocol = "Torznab" }},
		{"credential controls", func(i *ports.IndexerConfigurationInput) { i.Credential = "secret\n" }},
		{"credential invalid UTF8", func(i *ports.IndexerConfigurationInput) { i.Credential = "\xff" }},
		{"long credential", func(i *ports.IndexerConfigurationInput) { i.Credential = strings.Repeat("s", 8193) }},
		{"many categories", func(i *ports.IndexerConfigurationInput) { i.Categories = make([]int, 101) }},
		{"duplicate categories", func(i *ports.IndexerConfigurationInput) { i.Categories = []int{2000, 2000} }},
		{"zero category", func(i *ports.IndexerConfigurationInput) { i.Categories = []int{0} }},
		{"negative category", func(i *ports.IndexerConfigurationInput) { i.Categories = []int{-1} }},
		{"overflow category", func(i *ports.IndexerConfigurationInput) { i.Categories = []int{2147483648} }},
	}
	for name, raw := range map[string]string{
		"empty URL": "", "relative": "/private-secret", "scheme relative": "//localhost/path", "scheme": "ftp://localhost/private-secret",
		"opaque": "http:private-secret", "empty host": "https:///private-secret", "empty hostname": "https://:80/private-secret",
		"fragment": "https://localhost/#private-secret", "empty fragment": "https://localhost/#",
		"control": "https://localhost/secret\n", "encoded control": "https://localhost/secret%0d", "query control": "https://localhost/?x=%00",
		"space": "https://localhost/private secret", "invalid escape": "https://localhost/%zz", "query escape": "https://localhost/?key=%zz",
		"invalid port": "https://localhost:abc/", "empty port": "https://localhost:/", "zero port": "https://localhost:0/", "large port": "https://localhost:65536/",
		"negative port": "https://localhost:-1/", "unbracketed IPv6": "http://::1/", "invalid bracket host": "http://[secret]/", "invalid host": "http://host,secret/",
		"invalid UTF8": "https://localhost/\xff", "long URL": "https://localhost/" + strings.Repeat("s", 8192),
	} {
		cases = append(cases, struct {
			name   string
			change func(*ports.IndexerConfigurationInput)
		}{name, func(i *ports.IndexerConfigurationInput) { i.URL = raw }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.change(&input)
			codec := &configurationFailureCodec{failAt: 1}
			got, err := search.PrepareIndexerConfiguration(input, codec)
			if err != ports.ErrRejected || !reflect.DeepEqual(got, ports.PreparedIndexerConfiguration{}) || codec.calls != 0 {
				t.Fatal("invalid config must return safe rejection before encryption")
			}
		})
	}
}

func TestPrepareIndexerConfigurationBounds(t *testing.T) {
	t.Parallel()
	input := ports.IndexerConfigurationInput{Protocol: ports.IndexerProtocolTorznab, URL: "https://localhost/", Credential: strings.Repeat("s", 8192)}
	input.URL += strings.Repeat("s", 8192-len(input.URL))
	for i := 1; i <= 100; i++ {
		input.Categories = append(input.Categories, i)
	}
	input.Categories[99] = 2147483647
	got, err := search.PrepareIndexerConfiguration(input, configurationCodec())
	if err != nil || len(got.Categories) != 100 {
		t.Fatal("inclusive input bounds rejected")
	}
}

type configurationFailureCodec struct {
	calls  int
	failAt int
	empty  bool
}

func (c *configurationFailureCodec) EncryptSecret([]byte) ([]byte, error) {
	c.calls++
	if c.calls == c.failAt {
		if c.empty {
			return nil, nil
		}
		return []byte("partial-private-secret"), errors.New("codec private-secret low-level details")
	}
	return []byte("opaque-envelope"), nil
}
func (*configurationFailureCodec) DigestSecret([]byte) (string, error) {
	panic("preparation must only encrypt")
}

func TestPrepareIndexerConfigurationUnavailable(t *testing.T) {
	t.Parallel()
	input := ports.IndexerConfigurationInput{Protocol: ports.IndexerProtocolRSS, URL: "http://localhost/feed?passkey=private-secret"}
	for _, codec := range []ports.SecretProtector{nil, crypto.NewSecretProtector(nil), &configurationFailureCodec{failAt: 1}, &configurationFailureCodec{failAt: 2}, &configurationFailureCodec{failAt: 1, empty: true}, &configurationFailureCodec{failAt: 2, empty: true}} {
		got, err := search.PrepareIndexerConfiguration(input, codec)
		if err != ports.ErrUnavailable || !reflect.DeepEqual(got, ports.PreparedIndexerConfiguration{}) {
			t.Fatal("codec failure must return safe unavailable and no partial result")
		}
	}
}
