package seerr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"io"
	"strconv"
	"strings"
)

type Service struct{ repo ports.SeerrRepository }

func NewService(repo ports.SeerrRepository) *Service { return &Service{repo: repo} }
func admin(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || a.Scopes != nil || a.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	return nil
}
func (s *Service) Configuration(ctx context.Context, a *ports.Actor) (ports.SeerrConfiguration, error) {
	if e := admin(a); e != nil {
		return ports.SeerrConfiguration{}, e
	}
	return s.repo.ReadSeerrConfiguration(ctx, a)
}
func (s *Service) Save(ctx context.Context, a *ports.Actor, in ports.SeerrConfigurationInput) (ports.SeerrConfiguration, error) {
	if e := admin(a); e != nil {
		return ports.SeerrConfiguration{}, e
	}
	if in.ExpectedVersion < 0 || in.OwnerID == uuid.Nil || in.Credential != "" && !ValidCredential(in.Credential) {
		return ports.SeerrConfiguration{}, ports.ErrRejected
	}
	return s.repo.SaveSeerrConfiguration(ctx, a, in)
}
func (s *Service) Receipts(ctx context.Context, a *ports.Actor) ([]ports.SeerrReceipt, error) {
	if e := admin(a); e != nil {
		return nil, e
	}
	return s.repo.ListSeerrReceipts(ctx, a)
}
func ValidCredential(v string) bool {
	if len(v) < 32 || len(v) > 256 {
		return false
	}
	for _, r := range v {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
func (s *Service) Receive(ctx context.Context, credential string, raw []byte) (ports.SeerrReceipt, error) {
	if !ValidCredential(credential) {
		return ports.SeerrReceipt{}, ports.ErrUnauthorized
	}
	event, e := ParseEvent(raw)
	if e != nil {
		return ports.SeerrReceipt{}, e
	}
	return s.repo.ReceiveSeerr(ctx, credential, event)
}

// CheckJSON rejects duplicate names at every depth before typed binding. Seerr's
// standard notification envelope carries fields unused by this integration.
func CheckJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	fields := 0
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return ports.ErrRejected
		}
		t, e := d.Token()
		if e != nil {
			return ports.ErrRejected
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				name, ok := k.(string)
				fields++
				if e != nil || !ok || seen[name] || fields > 512 {
					return ports.ErrRejected
				}
				seen[name] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		case json.Delim('['):
			for d.More() {
				fields++
				if fields > 512 {
					return ports.ErrRejected
				}
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		}
		return nil
	}
	if e := walk(0); e != nil {
		return ports.ErrRejected
	}
	if _, e := d.Token(); e != io.EOF {
		return ports.ErrRejected
	}
	return nil
}
func ParseEvent(raw []byte) (ports.SeerrEvent, error) {
	var out ports.SeerrEvent
	if len(raw) == 0 || len(raw) > 64*1024 || CheckJSON(raw) != nil {
		return out, ports.ErrRejected
	}
	var in struct {
		Notification string `json:"notification_type"`
		Media        struct {
			Type string      `json:"media_type"`
			TMDB json.Number `json:"tmdbId"`
		} `json:"media"`
		Request struct {
			ID json.Number `json:"request_id"`
		} `json:"request"`
		Extra []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"extra"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Notification != "MEDIA_APPROVED" && in.Notification != "MEDIA_AUTO_APPROVED" {
		return out, ports.ErrRejected
	}
	id, e := in.Request.ID.Int64()
	tmdb, e2 := in.Media.TMDB.Int64()
	if e != nil || e2 != nil || id <= 0 || id > 9007199254740991 {
		return out, ports.ErrRejected
	}
	intent := mediarequest.Intent{MediaType: in.Media.Type, TMDBID: tmdb, Seasons: []int{}}
	if intent.MediaType == "tv" {
		found := false
		for _, v := range in.Extra {
			if v.Name != "Requested Seasons" {
				continue
			}
			if found {
				return out, ports.ErrRejected
			}
			found = true
			for _, part := range strings.Split(v.Value, ",") {
				n, e := strconv.Atoi(strings.TrimSpace(part))
				if e != nil || n < 0 {
					return out, ports.ErrRejected
				}
				intent.Seasons = append(intent.Seasons, n)
				intent.IncludeSpecials = intent.IncludeSpecials || n == 0
			}
		}
		if !found || len(intent.Seasons) == 0 {
			return out, ports.ErrRejected
		}
	}
	intent, e = mediarequest.CanonicalIntent(intent)
	if e != nil {
		return out, ports.ErrRejected
	}
	var payload any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&payload) != nil {
		return out, ports.ErrRejected
	}
	canonical, e := json.Marshal(payload)
	if e != nil {
		return out, ports.ErrRejected
	}
	hash := sha256.Sum256(canonical)
	return ports.SeerrEvent{ExternalRequestID: strconv.FormatInt(id, 10), PayloadHash: hex.EncodeToString(hash[:]), Intent: intent}, nil
}
