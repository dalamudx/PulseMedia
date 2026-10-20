package indexer_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
)

func TestSiteAccountAmountDisplayPrecision(t *testing.T) {
	for _, tc := range []struct{ raw, value, unit string }{
		{"0.00 KB", "0.00", "KB"}, {"1.00 GB", "1.00", "GB"},
		{"1,234.56 MB", "1234.56", "MB"}, {"0.980 TB", "0.980", "TB"},
		{"9,007,199,254,740,993.001 PB", "9007199254740993.001", "PB"},
		{" 1.00\u00a0GB ", "1.00", "GB"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := indexer.ParseSiteAccountAmount(tc.raw)
			if err != nil || got.Value != tc.value || got.Unit != tc.unit || !got.Approximate {
				t.Fatalf("%+v %v", got, err)
			}
			raw, err := json.Marshal(got)
			if err != nil || strings.Contains(string(raw), "bytes") {
				t.Fatal("display became exact byte evidence")
			}
		})
	}
	for _, raw := range []string{"", "1.00", "1.00 GiB", "1.00 B", "1.00 gb", "1 GB", "1.000 GB", "1.00 TB", "01.00 GB", "0,001.00 GB", "1,23.00 GB", "1.234,00 GB", "1,2345.00 GB", "1,,234.00 GB", "-1.00 GB", "+1.00 GB", "NaN GB", "Inf. GB", "1e2 GB", "1. 00 GB", "1.00\nGB", "1.00\x00 GB", strings.Repeat("9", 37) + ".00 GB", strings.Repeat(" ", 129) + "1.00 GB"} {
		t.Run("reject_"+raw, func(t *testing.T) {
			if v, e := indexer.ParseSiteAccountAmount(raw); v != nil || !errors.Is(e, indexer.ErrInvalidAccountStats) {
				t.Fatalf("%+v %v", v, e)
			}
		})
	}
}

func TestSiteAccountRatioAndBonus(t *testing.T) {
	for _, tc := range []struct{ raw, state, value string }{
		{"", indexer.SiteRatioUnknown, ""}, {"---", indexer.SiteRatioUndefined, ""},
		{"Inf.", indexer.SiteRatioInfinite, ""}, {"无限", indexer.SiteRatioInfinite, ""}, {"無限", indexer.SiteRatioInfinite, ""},
		{"0.000", indexer.SiteRatioFinite, "0.000"}, {"1.235", indexer.SiteRatioFinite, "1.235"},
		{"1,234.500", indexer.SiteRatioFinite, "1234.500"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			v, e := indexer.ParseSiteAccountRatio(tc.raw)
			if e != nil || v.State != tc.state || v.Value != tc.value {
				t.Fatalf("%+v %v", v, e)
			}
		})
	}
	for _, raw := range []string{"0", "0.00", "0.0000", "Infinity", "∞", "-1.000", "NaN", "1e3", "1,23.000", "01.000", strings.Repeat("9", 37) + ".000"} {
		t.Run("ratio_reject_"+raw, func(t *testing.T) {
			if _, e := indexer.ParseSiteAccountRatio(raw); !errors.Is(e, indexer.ErrInvalidAccountStats) {
				t.Fatal(e)
			}
		})
	}
	for _, tc := range []struct{ raw, want string }{{"0.0", "0.0"}, {"12,345.6", "12345.6"}, {"9007199254740993.1", "9007199254740993.1"}} {
		t.Run("bonus_"+tc.raw, func(t *testing.T) {
			v, e := indexer.ParseSiteAccountBonus(tc.raw)
			if e != nil || v != tc.want {
				t.Fatalf("%q %v", v, e)
			}
		})
	}
	for _, raw := range []string{"", "0", "1.00", "1,23.4", "01.0", "-0.0", "NaN", "1e3", strings.Repeat("9", 37) + ".0"} {
		t.Run("bonus_reject_"+raw, func(t *testing.T) {
			if _, e := indexer.ParseSiteAccountBonus(raw); !errors.Is(e, indexer.ErrInvalidAccountStats) {
				t.Fatal(e)
			}
		})
	}
}

func TestSiteAccountIdentityAndCanonicalValidation(t *testing.T) {
	for _, id := range []string{"1", "12345", "18446744073709551615"} {
		if !indexer.ValidSiteAccountID(id) {
			t.Fatal(id)
		}
	}
	for _, id := range []string{"", "0", "00", "01", "-1", "+1", "1 ", " 1", "1.0", "18446744073709551616", "user", "１２"} {
		if indexer.ValidSiteAccountID(id) {
			t.Fatal(id)
		}
	}
	unknown := indexer.SiteAccountStats{AccountID: "12345", Ratio: indexer.SiteAccountRatio{State: indexer.SiteRatioUnknown}}
	if e := indexer.ValidateSiteAccountStats(unknown); e != nil {
		t.Fatal(e)
	}
	amount, _ := indexer.ParseSiteAccountAmount("0.00 KB")
	bonus := "0.0"
	full := indexer.SiteAccountStats{AccountID: "12345", Uploaded: amount, Downloaded: amount, Ratio: indexer.SiteAccountRatio{State: indexer.SiteRatioInfinite}, Bonus: &bonus}
	if e := indexer.ValidateSiteAccountStats(full); e != nil {
		t.Fatal(e)
	} // Rounded download zero does not override source infinity.
	for _, tc := range []struct {
		name   string
		change func(*indexer.SiteAccountStats)
	}{
		{"identity", func(s *indexer.SiteAccountStats) { s.AccountID = "012345" }},
		{"empty_state", func(s *indexer.SiteAccountStats) { s.Ratio = indexer.SiteAccountRatio{} }},
		{"unknown_value", func(s *indexer.SiteAccountStats) {
			s.Ratio = indexer.SiteAccountRatio{State: indexer.SiteRatioUnknown, Value: "0.000"}
		}},
		{"finite_empty", func(s *indexer.SiteAccountStats) { s.Ratio = indexer.SiteAccountRatio{State: indexer.SiteRatioFinite} }},
		{"finite_noncanonical", func(s *indexer.SiteAccountStats) {
			s.Ratio = indexer.SiteAccountRatio{State: indexer.SiteRatioFinite, Value: "1,234.500"}
		}},
		{"exact_claim", func(s *indexer.SiteAccountStats) { x := *s.Uploaded; x.Approximate = false; s.Uploaded = &x }},
		{"grouped_value", func(s *indexer.SiteAccountStats) {
			s.Uploaded = &indexer.SiteAccountAmount{Value: "1,000.00", Unit: "GB", Approximate: true}
		}},
		{"bad_bonus", func(s *indexer.SiteAccountStats) { x := "0.00"; s.Bonus = &x }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := full
			tc.change(&v)
			if e := indexer.ValidateSiteAccountStats(v); !errors.Is(e, indexer.ErrInvalidAccountStats) {
				t.Fatal(e)
			}
		})
	}
	if e := indexer.ValidateSiteAccountStats(full); e != nil {
		t.Fatal("validation changed original", e)
	}
	raw, e := json.Marshal(unknown)
	if e != nil || !strings.Contains(string(raw), `"uploaded":null`) || !strings.Contains(string(raw), `"bonus":null`) {
		t.Fatal("unknown lost", string(raw), e)
	}
}
