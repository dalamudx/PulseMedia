package cookiecloud

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type CookieItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Client struct {
	httpClient *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{httpClient: httpClient}
}

// DeriveKey generates MD5 key/iv for CookieCloud payload decryption.
func DeriveKey(userUUID, password string) []byte {
	raw := fmt.Sprintf("%s-%s", userUUID, password)
	hash := md5.Sum([]byte(raw))
	hexStr := hex.EncodeToString(hash[:])
	return []byte(hexStr[:16])
}

// DecryptPayload decrypts CookieCloud AES-128-CBC ciphertext.
func DecryptPayload(ciphertext, key []byte) ([]byte, error) {
	if len(ciphertext) < aes.BlockSize {
		return nil, errors.New("ciphertext too short")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	iv := key // CookieCloud uses the 16-byte key as IV
	mode := cipher.NewCBCDecrypter(block, iv)

	plain := make([]byte, len(ciphertext))
	mode.CryptBlocks(plain, ciphertext)

	// PKCS7 Unpad
	length := len(plain)
	if length == 0 {
		return nil, errors.New("empty plain text")
	}
	unpadding := int(plain[length-1])
	if unpadding > length || unpadding > aes.BlockSize {
		return plain, nil // return as is if no padding
	}

	return plain[:(length - unpadding)], nil
}

// SyncCookies fetches and parses site cookies from CookieCloud server.
func (c *Client) SyncCookies(ctx context.Context, serverURL, userUUID, password string) (map[string]string, error) {
	if serverURL == "" || userUUID == "" {
		return nil, fmt.Errorf("serverURL and userUUID cannot be empty")
	}

	serverURL = strings.TrimRight(serverURL, "/")
	reqURL := fmt.Sprintf("%s/get/%s", serverURL, userUUID)

	passKey := DeriveKey(userUUID, password)
	reqBody, _ := json.Marshal(map[string]string{
		"password": string(passKey),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ports.ErrTimeout
		}
		return nil, fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	var res struct {
		CookieData map[string][]CookieItem `json:"cookie_data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("%w: failed to parse cookiecloud response: %v", ports.ErrBadResponse, err)
	}

	// Format each domain into Cookie string: "name1=val1; name2=val2"
	domainCookies := make(map[string]string)
	for domain, items := range res.CookieData {
		var pairs []string
		for _, item := range items {
			pairs = append(pairs, fmt.Sprintf("%s=%s", item.Name, item.Value))
		}
		domainCookies[domain] = strings.Join(pairs, "; ")
	}

	return domainCookies, nil
}
