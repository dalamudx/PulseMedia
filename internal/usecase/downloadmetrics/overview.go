package downloadmetrics

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

const globalRatesFreshness = 45 * time.Second
const globalRatesHistory = 10 * time.Minute

type OverviewService struct {
	reader  ports.DownloadGlobalRatesOverviewReader
	enabled bool
}

func NewOverviewService(reader ports.DownloadGlobalRatesOverviewReader, enabled bool) *OverviewService {
	return &OverviewService{reader: reader, enabled: enabled}
}
func (s *OverviewService) Read(ctx context.Context, actor *ports.Actor) (*ports.DownloadGlobalRatesOverview, error) {
	if actor == nil || actor.Type != ports.ActorTypeUser || actor.Role != ports.RoleAdmin || actor.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if id, err := uuid.Parse(actor.ID); err != nil || id == uuid.Nil {
		return nil, ports.ErrForbidden
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.reader == nil {
		return nil, ports.ErrUnavailable
	}
	snapshot, err := s.reader.ReadDownloadGlobalRatesSnapshot(ctx, actor)
	if err != nil {
		if errors.Is(err, ports.ErrForbidden) {
			return nil, ports.ErrForbidden
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ports.ErrUnavailable
	}
	if snapshot == nil || snapshot.ObservedAt.IsZero() {
		return nil, ports.ErrUnavailable
	}
	return projectOverview(snapshot, s.enabled)
}
func sampleTime(sample *ports.DownloadGlobalRateSample) *time.Time {
	if sample == nil {
		return nil
	}
	if sample.ObservedAt != nil {
		return sample.ObservedAt
	}
	return sample.FinishedAt
}
func sampleState(source ports.DownloadGlobalRateSource, sample *ports.DownloadGlobalRateSample, at time.Time) (string, *int64, *int64) {
	if sample == nil {
		return "unobserved", nil, nil
	}
	if sample.ClientID != source.ID || sample.ConfigStamp != source.ConfigStamp {
		return "configuration_changed", nil, nil
	}
	when := sampleTime(sample)
	if when == nil || when.IsZero() || when.After(at) {
		return "unknown", nil, nil
	}
	if sample.State == "failed" || sample.State == "abandoned" {
		return "failed", nil, nil
	}
	if sample.State != "succeeded" {
		return "unknown", nil, nil
	}
	if at.Sub(*when) > globalRatesFreshness {
		return "stale", nil, nil
	}
	if (sample.DownloadBytesPerSecond != nil && *sample.DownloadBytesPerSecond < 0) || (sample.UploadBytesPerSecond != nil && *sample.UploadBytesPerSecond < 0) {
		return "unknown", nil, nil
	}
	return "observed", sample.DownloadBytesPerSecond, sample.UploadBytesPerSecond
}
func rateString(value *int64) *string {
	if value == nil || *value < 0 {
		return nil
	}
	text := strconv.FormatInt(*value, 10)
	return &text
}

// Every current source must contribute for a direction to have a global total.
// Arbitrary-precision addition preserves sums above the int64 boundary.
func aggregateAt(sources []ports.DownloadGlobalRateSource, samples map[uuid.UUID]*ports.DownloadGlobalRateSample, at time.Time) ports.DownloadGlobalRatePoint {
	down, up := new(big.Int), new(big.Int)
	point := ports.DownloadGlobalRatePoint{At: at}
	for _, source := range sources {
		_, d, u := sampleState(source, samples[source.ID], at)
		if d != nil {
			down.Add(down, big.NewInt(*d))
			point.DownloadKnownClients++
		}
		if u != nil {
			up.Add(up, big.NewInt(*u))
			point.UploadKnownClients++
		}
	}
	if point.DownloadKnownClients == len(sources) {
		v := down.String()
		point.DownloadBytesPerSecond = &v
	}
	if point.UploadKnownClients == len(sources) {
		v := up.String()
		point.UploadBytesPerSecond = &v
	}
	return point
}
func projectOverview(snapshot *ports.DownloadGlobalRatesSnapshot, enabled bool) (*ports.DownloadGlobalRatesOverview, error) {
	out := &ports.DownloadGlobalRatesOverview{Enabled: enabled, ObservedAt: snapshot.ObservedAt.UTC(), FreshnessSeconds: int(globalRatesFreshness / time.Second), Clients: []ports.DownloadGlobalRateClientView{}, Series: []ports.DownloadGlobalRatePoint{}}
	seen := make(map[uuid.UUID]bool, len(snapshot.Sources))
	current := make(map[uuid.UUID]*ports.DownloadGlobalRateSample, len(snapshot.Sources))
	for _, source := range snapshot.Sources {
		stamp, err := hex.DecodeString(source.ConfigStamp)
		if source.ID == uuid.Nil || seen[source.ID] || err != nil || len(stamp) != 32 || source.ConfigStamp != strings.ToLower(source.ConfigStamp) || (source.ClientType != "qbittorrent" && source.ClientType != "transmission") {
			return nil, ports.ErrUnavailable
		}
		seen[source.ID] = true
		state, down, up := sampleState(source, source.Latest, out.ObservedAt)
		item := ports.DownloadGlobalRateClientView{ID: source.ID, Name: source.Name, ClientType: source.ClientType, State: state, DownloadBytesPerSecond: rateString(down), UploadBytesPerSecond: rateString(up)}
		if source.Latest != nil && source.Latest.ConfigStamp == source.ConfigStamp && source.Latest.ClientID == source.ID {
			if when := sampleTime(source.Latest); when != nil && !when.After(out.ObservedAt) {
				v := when.UTC()
				item.ObservedAt = &v
			}
			if state == "failed" {
				item.FailureCode = source.Latest.FailureCode
			}
		}
		good := source.LastGood
		if good != nil && good.ClientID == source.ID && good.ConfigStamp == source.ConfigStamp && good.State == "succeeded" && good.ObservedAt != nil && !good.ObservedAt.After(out.ObservedAt) {
			item.LastGood = &ports.DownloadGlobalRateValues{ObservedAt: good.ObservedAt.UTC(), DownloadBytesPerSecond: rateString(good.DownloadBytesPerSecond), UploadBytesPerSecond: rateString(good.UploadBytesPerSecond)}
		}
		if !enabled {
			item.State = "disabled"
			item.DownloadBytesPerSecond = nil
			item.UploadBytesPerSecond = nil
			item.FailureCode = ""
		} else {
			current[source.ID] = source.Latest
		}
		out.Clients = append(out.Clients, item)
	}
	if !enabled {
		return out, nil
	}
	point := aggregateAt(snapshot.Sources, current, out.ObservedAt)
	out.DownloadBytesPerSecond = point.DownloadBytesPerSecond
	out.UploadBytesPerSecond = point.UploadBytesPerSecond
	out.DownloadKnownClients = point.DownloadKnownClients
	out.UploadKnownClients = point.UploadKnownClients
	samples := append([]ports.DownloadGlobalRateSample{}, snapshot.History...)
	for _, source := range snapshot.Sources {
		if source.Latest != nil {
			samples = append(samples, *source.Latest)
		}
	}
	sort.SliceStable(samples, func(i, j int) bool {
		a, b := sampleTime(&samples[i]), sampleTime(&samples[j])
		if a == nil {
			return b != nil
		}
		if b == nil {
			return false
		}
		if !a.Equal(*b) {
			return a.Before(*b)
		}
		return samples[i].JobID.String() < samples[j].JobID.String()
	})
	cursor := 0
	historical := map[uuid.UUID]*ports.DownloadGlobalRateSample{}
	start := out.ObservedAt.Truncate(10 * time.Second).Add(-globalRatesHistory)
	for at := start; !at.After(out.ObservedAt); at = at.Add(10 * time.Second) {
		for cursor < len(samples) {
			sample := &samples[cursor]
			when := sampleTime(sample)
			if when != nil && when.After(at) {
				break
			}
			if when != nil && seen[sample.ClientID] {
				historical[sample.ClientID] = sample
			}
			cursor++
		}
		out.Series = append(out.Series, aggregateAt(snapshot.Sources, historical, at))
	}
	if len(out.Series) == 0 || !out.Series[len(out.Series)-1].At.Equal(out.ObservedAt) {
		out.Series = append(out.Series, point)
	}
	return out, nil
}
