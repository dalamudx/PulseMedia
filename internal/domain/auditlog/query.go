package auditlog

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrInvalidQuery = errors.New("invalid audit query")

const DefaultLimit = 50
const MaximumLimit = 200

// Filter uses exact matches. Until is exclusive; From is inclusive.
type Filter struct {
	ActorType  string    `json:"actor_type"`
	ActorID    string    `json:"actor_id"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	From       time.Time `json:"from"`
	Until      time.Time `json:"until"`
}

type Position struct {
	CreatedAt time.Time `json:"created_at"`
	ID        uuid.UUID `json:"id"`
}

func timestampOK(t time.Time) bool {
	return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999 && t.Nanosecond()%1000 == 0
}

func Normalize(f Filter) (Filter, error) {
	if f.ActorType != "" && f.ActorType != "user" && f.ActorType != "service" && f.ActorType != "system" {
		return Filter{}, ErrInvalidQuery
	}
	for _, s := range []string{f.ActorID, f.Action, f.TargetType, f.TargetID} {
		if len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) != s || strings.ContainsFunc(s, unicode.IsControl) {
			return Filter{}, ErrInvalidQuery
		}
	}
	for _, t := range []time.Time{f.From, f.Until} {
		if !t.IsZero() && !timestampOK(t) {
			return Filter{}, ErrInvalidQuery
		}
	}
	if !f.From.IsZero() && !f.Until.IsZero() && !f.From.Before(f.Until) {
		return Filter{}, ErrInvalidQuery
	}
	f.From, f.Until = f.From.UTC(), f.Until.UTC()
	return f, nil
}

func (p Position) Valid() bool { return p.ID != uuid.Nil && timestampOK(p.CreatedAt) }

// Before implements descending (created_at, id) pagination without an offset.
func (p Position) Before(other Position) bool {
	if !p.CreatedAt.Equal(other.CreatedAt) {
		return p.CreatedAt.Before(other.CreatedAt)
	}
	return strings.Compare(p.ID.String(), other.ID.String()) < 0
}

type cursor struct {
	Version    int       `json:"v"`
	Account    uuid.UUID `json:"account"`
	FilterHash string    `json:"filter"`
	Position   Position  `json:"position"`
}

func fingerprint(f Filter) (string, error) {
	f, err := Normalize(f)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return "", ErrInvalidQuery
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Cursors bind a position to its account and filters, never grant read authority.
// Live authorization is required for every page, including decoded cursors.
func EncodeCursor(account uuid.UUID, f Filter, p Position) (string, error) {
	if account == uuid.Nil || !p.Valid() {
		return "", ErrInvalidQuery
	}
	hash, err := fingerprint(f)
	if err != nil {
		return "", err
	}
	p.CreatedAt = p.CreatedAt.UTC()
	b, err := json.Marshal(cursor{Version: 1, Account: account, FilterHash: hash, Position: p})
	if err != nil {
		return "", ErrInvalidQuery
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func DecodeCursor(value string, account uuid.UUID, f Filter) (*Position, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > 1024 || account == uuid.Nil {
		return nil, ErrInvalidQuery
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidQuery
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil || c.Version != 1 || c.Account != account || !c.Position.Valid() {
		return nil, ErrInvalidQuery
	}
	canonical, err := EncodeCursor(account, f, c.Position)
	// Canonical round-trip also rejects unknown fields, duplicate keys and aliases.
	if err != nil || canonical != value {
		return nil, ErrInvalidQuery
	}
	return &c.Position, nil
}
