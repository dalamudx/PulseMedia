// Package seeding evaluates eligibility only; it never grants an execution permit.
package seeding

import (
	"errors"
	"math"
)

var ErrInvalidRule = errors.New("invalid seeding rule")
var ErrInvalidObservation = errors.New("invalid native seeding observation")

type MatchMode string

const (
	MatchAny MatchMode = "any"
	MatchAll MatchMode = "all"
)

type Rule struct {
	Mode              MatchMode `json:"mode"`
	MinSeedingSeconds *int64    `json:"min_seeding_seconds,omitempty"`
	MinShareRatio     *float64  `json:"min_share_ratio,omitempty"`
	LowFreeBytes      *int64    `json:"low_free_bytes,omitempty"`
	DeleteFiles       bool      `json:"delete_files"`
}

// Facts are native observations. Nil is unknown; neither elapsed Add time nor
// upload/download byte division establishes seeding time or native share ratio.
type Observation struct {
	Complete                     bool
	SeedingSeconds               *int64
	ShareRatio                   *float64
	InfiniteShareRatio           bool
	FreeBytes                    *int64
	FreeSpaceMatchesDownloadPath bool
}

// Protection comes from the persisted task graph, never client input.
type Protection struct {
	Owned       bool
	Imported    bool
	PendingWork bool
}
type Decision struct {
	Eligible bool
	Reason   string
}

func (r Rule) Validate() error {
	if r.Mode != MatchAny && r.Mode != MatchAll || r.DeleteFiles || r.MinSeedingSeconds == nil && r.MinShareRatio == nil && r.LowFreeBytes == nil {
		return ErrInvalidRule
	}
	if r.MinSeedingSeconds != nil && (*r.MinSeedingSeconds <= 0 || *r.MinSeedingSeconds > 315360000) {
		return ErrInvalidRule
	}
	if r.MinShareRatio != nil && (math.IsNaN(*r.MinShareRatio) || math.IsInf(*r.MinShareRatio, 0) || *r.MinShareRatio <= 0 || *r.MinShareRatio > 1000000) {
		return ErrInvalidRule
	}
	if r.LowFreeBytes != nil && *r.LowFreeBytes <= 0 {
		return ErrInvalidRule
	}
	return nil
}
func Evaluate(o *Observation, p Protection, r Rule) (Decision, error) {
	if err := r.Validate(); err != nil {
		return Decision{}, err
	}
	if !p.Owned {
		return Decision{Reason: "not_owned"}, nil
	}
	if !p.Imported {
		return Decision{Reason: "not_imported"}, nil
	}
	if p.PendingWork {
		return Decision{Reason: "pending_work"}, nil
	}
	if o == nil {
		return Decision{Reason: "unknown_observation"}, nil
	}
	if o.SeedingSeconds != nil && *o.SeedingSeconds < 0 || o.FreeBytes != nil && *o.FreeBytes < 0 || o.ShareRatio != nil && (math.IsNaN(*o.ShareRatio) || math.IsInf(*o.ShareRatio, 0) || *o.ShareRatio < 0 || o.InfiniteShareRatio) {
		return Decision{}, ErrInvalidObservation
	}
	if !o.Complete {
		return Decision{Reason: "incomplete"}, nil
	}
	matched, total, unknown := 0, 0, false
	check := func(known, met bool) {
		total++
		if !known {
			unknown = true
		}
		if known && met {
			matched++
		}
	}
	if r.MinSeedingSeconds != nil {
		known := o.SeedingSeconds != nil
		check(known, known && *o.SeedingSeconds >= *r.MinSeedingSeconds)
	}
	if r.MinShareRatio != nil {
		known := o.ShareRatio != nil || o.InfiniteShareRatio
		check(known, o.InfiniteShareRatio || o.ShareRatio != nil && *o.ShareRatio >= *r.MinShareRatio)
	}
	if r.LowFreeBytes != nil {
		known := o.FreeBytes != nil && o.FreeSpaceMatchesDownloadPath
		check(known, known && *o.FreeBytes < *r.LowFreeBytes)
	}
	if r.Mode == MatchAny && matched > 0 || r.Mode == MatchAll && matched == total {
		return Decision{Eligible: true, Reason: "criteria_met_keep_data"}, nil
	}
	if unknown {
		return Decision{Reason: "unknown_facts"}, nil
	}
	return Decision{Reason: "criteria_not_met"}, nil
}
