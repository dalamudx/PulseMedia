package indexer

import (
	"errors"
	"strconv"
	"strings"
)

var ErrInvalidAccountStats = errors.New("invalid site account statistics")

const (
	SiteRatioFinite    = "finite"
	SiteRatioInfinite  = "infinite"
	SiteRatioUndefined = "undefined"
	SiteRatioUnknown   = "unknown"
)

// SiteAccountAmount is a rounded source display, not an exact byte counter.
type SiteAccountAmount struct {
	Value       string `json:"value"`
	Unit        string `json:"unit"`
	Approximate bool   `json:"approximate"`
}

type SiteAccountRatio struct {
	State string `json:"state"`
	Value string `json:"value,omitempty"`
}

type SiteAccountStats struct {
	AccountID  string             `json:"account_id"`
	Uploaded   *SiteAccountAmount `json:"uploaded"`
	Downloaded *SiteAccountAmount `json:"downloaded"`
	Ratio      SiteAccountRatio   `json:"ratio"`
	Bonus      *string            `json:"bonus"`
}

func ValidSiteAccountID(value string) bool {
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == value
}

// NexusPHP uses comma grouping and a decimal point, including Chinese locales.
func accountDecimal(raw string, scale int) (string, error) {
	if len(raw) > 128 {
		return "", ErrInvalidAccountStats
	}
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(parts[1]) != scale {
		return "", ErrInvalidAccountStats
	}
	groups := strings.Split(parts[0], ",")
	if len(groups) > 1 {
		if len(groups[0]) < 1 || len(groups[0]) > 3 || groups[0][0] == '0' {
			return "", ErrInvalidAccountStats
		}
		for _, group := range groups[1:] {
			if len(group) != 3 {
				return "", ErrInvalidAccountStats
			}
		}
	}
	integer := strings.Join(groups, "")
	if len(integer) == 0 || len(integer) > 1 && integer[0] == '0' || len(integer)+scale > 36 {
		return "", ErrInvalidAccountStats
	}
	for _, r := range integer + parts[1] {
		if r < '0' || r > '9' {
			return "", ErrInvalidAccountStats
		}
	}
	return integer + "." + parts[1], nil
}

func ParseSiteAccountAmount(raw string) (*SiteAccountAmount, error) {
	if len(raw) > 128 {
		return nil, ErrInvalidAccountStats
	}
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return nil, ErrInvalidAccountStats
	}
	for _, r := range strings.TrimSpace(raw) {
		if r < 32 || r == 127 {
			return nil, ErrInvalidAccountStats
		}
	}
	scale := 2
	switch fields[1] {
	case "KB", "MB", "GB":
	case "TB", "PB":
		scale = 3
	default:
		return nil, ErrInvalidAccountStats
	}
	value, err := accountDecimal(fields[0], scale)
	if err != nil {
		return nil, err
	}
	return &SiteAccountAmount{Value: value, Unit: fields[1], Approximate: true}, nil
}

func ParseSiteAccountBonus(raw string) (string, error) { return accountDecimal(raw, 1) }

func ParseSiteAccountRatio(raw string) (SiteAccountRatio, error) {
	if len(raw) > 128 {
		return SiteAccountRatio{}, ErrInvalidAccountStats
	}
	switch strings.TrimSpace(raw) {
	case "":
		return SiteAccountRatio{State: SiteRatioUnknown}, nil
	case "---":
		return SiteAccountRatio{State: SiteRatioUndefined}, nil
	case "Inf.", "无限", "無限":
		return SiteAccountRatio{State: SiteRatioInfinite}, nil
	default:
		value, err := accountDecimal(raw, 3)
		if err != nil {
			return SiteAccountRatio{}, err
		}
		return SiteAccountRatio{State: SiteRatioFinite, Value: value}, nil
	}
}

func ValidateSiteAccountStats(stats SiteAccountStats) error {
	if !ValidSiteAccountID(stats.AccountID) {
		return ErrInvalidAccountStats
	}
	for _, amount := range []*SiteAccountAmount{stats.Uploaded, stats.Downloaded} {
		if amount == nil {
			continue
		}
		parsed, err := ParseSiteAccountAmount(amount.Value + " " + amount.Unit)
		if err != nil || !amount.Approximate || *parsed != *amount {
			return ErrInvalidAccountStats
		}
	}
	switch stats.Ratio.State {
	case SiteRatioUnknown, SiteRatioUndefined, SiteRatioInfinite:
		if stats.Ratio.Value != "" {
			return ErrInvalidAccountStats
		}
	case SiteRatioFinite:
		parsed, err := ParseSiteAccountRatio(stats.Ratio.Value)
		if err != nil || parsed != stats.Ratio {
			return ErrInvalidAccountStats
		}
	default:
		return ErrInvalidAccountStats
	}
	if stats.Bonus != nil {
		parsed, err := ParseSiteAccountBonus(*stats.Bonus)
		if err != nil || parsed != *stats.Bonus {
			return ErrInvalidAccountStats
		}
	}
	return nil
}
