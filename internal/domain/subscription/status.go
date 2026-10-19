// Package subscription derives read-only status from an explicitly observed snapshot.
// It never authorizes acquisition or changes reservations/projections.
package subscription

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
	"time"
)

type FileEvidence = quality.FileEvidence
type RoundEvidence struct {
	State, Reason                 string
	IntentVersion, PolicyRevision int64
	Deadline                      time.Time
}
type UnitInput struct {
	ID, UnitID                   uuid.UUID
	Key                          string
	Skipped, Retired             bool
	AirDate                      *time.Time
	FactsVersion                 int64
	MetadataVersion              int64
	Files                        []FileEvidence
	SharedAcquisition            bool
	Reserved, ReservationUnknown bool
	FactStage                    string
	Round                        *RoundEvidence
}
type Snapshot struct {
	ID                                            uuid.UUID
	State, MediaType                              string
	IntentVersion, PolicyRevision, RootGeneration int64
	RootHealthRevision, MediaMetadataVersion      int64
	ObservedAt                                    time.Time
	RootHealth                                    string
	BaselineCompleted, PolicyAvailable            bool
	Rules                                         quality.PolicyRules
	BoundIndexers, AvailableIndexers              int
	EndpointsReady                                bool
	EndpointRetryAt                               *time.Time
	DownloaderAvailable                           bool
	Units                                         []UnitInput
}
type UnitStatus struct {
	ID                   uuid.UUID `json:"id"`
	UnitID               uuid.UUID `json:"unit_id"`
	UnitKey              string    `json:"unit_key"`
	FactsVersion         int64     `json:"facts_version"`
	ReservationProtected bool      `json:"reservation_protected"`
	IsSkipped            bool      `json:"is_skipped"`
	IsSatisfied          bool      `json:"is_satisfied"`
	Status               string    `json:"status"`
	WaitReason           string    `json:"wait_reason,omitempty"`
	CurrentFactStage     string    `json:"current_fact_stage,omitempty"`
}
type StatusReport struct {
	ID              uuid.UUID    `json:"id"`
	IntentVersion   int64        `json:"intent_version"`
	PolicyRevision  int64        `json:"policy_revision"`
	RootGeneration  int64        `json:"root_generation"`
	ObservedAt      time.Time    `json:"observed_at"`
	Blockers        []string     `json:"blockers"`
	EndpointRetryAt *time.Time   `json:"endpoint_retry_at,omitempty"`
	Items           []UnitStatus `json:"items"`
}

func DeriveStatus(s Snapshot) StatusReport { return deriveStatus(s, true) }

// DeriveProjection uses the same ownership/control rules, but does not conflate
// missing files with the separately checked network automation prerequisites.
func DeriveProjection(s Snapshot) StatusReport { return deriveStatus(s, false) }
func deriveStatus(s Snapshot, automation bool) StatusReport {
	r := StatusReport{ID: s.ID, IntentVersion: s.IntentVersion, PolicyRevision: s.PolicyRevision, RootGeneration: s.RootGeneration, ObservedAt: s.ObservedAt, Blockers: []string{}, Items: []UnitStatus{}, EndpointRetryAt: s.EndpointRetryAt}
	add := func(condition bool, reason string) {
		if condition {
			r.Blockers = append(r.Blockers, reason)
		}
	}
	add(s.State != "active", "subscription_"+s.State)
	add(s.RootHealth != "online", "root_offline")
	add(s.RootHealth == "online" && !s.BaselineCompleted, "baseline_pending")
	add(!s.PolicyAvailable, "policy_unavailable")
	if automation {
		add(s.MediaType != "movie" && s.MediaType != "tv", "automation_unavailable")
		add(s.BoundIndexers == 0, "no_indexers")
		add(s.BoundIndexers > 0 && s.AvailableIndexers == 0, "endpoints_offline")
		add(s.AvailableIndexers > 0 && !s.EndpointsReady, "endpoint_backoff")
		add(!s.DownloaderAvailable, "no_downloader")
	}
	for _, u := range s.Units {
		v := UnitStatus{ID: u.ID, UnitID: u.UnitID, UnitKey: u.Key, FactsVersion: u.FactsVersion, IsSkipped: u.Skipped, ReservationProtected: u.Reserved, Status: "missing"}
		wait := func(status, reason string) { v.Status = status; v.WaitReason = reason }
		switch {
		case u.Skipped:
			wait("skipped", "skipped")
		case s.RootHealth != "online":
			wait("unknown", "root_offline")
		case !s.BaselineCompleted:
			wait("unknown", "baseline_pending")
		case !s.PolicyAvailable:
			wait("unknown", "policy_unavailable")
		case u.Retired:
			wait("waiting", "unit_retired")
		case u.AirDate != nil && u.AirDate.After(s.ObservedAt):
			wait("waiting", "future_air_date")
		default:
			owned := quality.AssessOwnership(s.Rules, u.Files)
			if owned.Owned {
				v.IsSatisfied = true
				v.Status = "satisfied"
				v.CurrentFactStage = "imported"
			}
			if !v.IsSatisfied {
				switch {
				case u.SharedAcquisition:
					if u.ReservationUnknown {
						wait("in_flight", "shared_acquisition_unknown")
					} else {
						wait("waiting", "shared_acquisition_available")
					}
				case u.Reserved:
					v.CurrentFactStage = u.FactStage
					if u.ReservationUnknown {
						wait("in_flight", "reservation_unknown")
					} else {
						wait("in_flight", "reservation_active")
					}
				case owned.Unknown:
					wait("unknown", "quality_unknown")
				case owned.Rejected:
					wait("missing", "file_rejected")
				case s.MediaType == "tv" && u.AirDate == nil:
					wait("waiting", "air_date_unknown")
				case len(r.Blockers) > 0:
					wait("waiting", r.Blockers[0])
				case u.Round != nil && u.Round.IntentVersion == s.IntentVersion && u.Round.PolicyRevision == s.PolicyRevision && u.Round.State == "running":
					if u.Round.Deadline.After(s.ObservedAt) {
						wait("waiting", "search_round_pending")
					} else {
						wait("waiting", "round_finalization_pending")
					}
				case u.Round != nil && u.Round.IntentVersion == s.IntentVersion && u.Round.PolicyRevision == s.PolicyRevision && u.Round.State == "expired" && u.Round.Reason == "no_qualified_candidate":
					wait("missing", "no_qualified_candidate")
				default:
					wait("missing", "missing")
				}
			}
		}
		r.Items = append(r.Items, v)
	}
	return r
}
