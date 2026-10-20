package indexer

import "testing"

func TestCheckinSchedule(t *testing.T) {
	for _, tc := range []struct {
		zone, at string
		valid    bool
	}{
		{"UTC", "00:00", true},
		{"Asia/Shanghai", "23:59", true},
		{"America/New_York", "02:30", true},
		{"Etc/GMT+8", "12:00", true},
		{"PRC", "08:05", true},
		// A well-formed unknown name still needs the repository's registry check.
		{"Never/Exists", "08:05", true},
		{"", "08:00", false},
		{"Local", "08:00", false},
		{"Factory", "08:00", false},
		{"localtime", "08:00", false},
		{"posixrules", "08:00", false},
		{"posix/UTC", "08:00", false},
		{"right/UTC", "08:00", false},
		{"/UTC", "08:00", false},
		{"Asia//Shanghai", "08:00", false},
		{"Asia/../Shanghai", "08:00", false},
		{"Asia/Shanghai ", "08:00", false},
		{"Asia/上海", "08:00", false},
		{"UTC\x00", "08:00", false},
		{"UTC", "0:00", false},
		{"UTC", "00:0", false},
		{"UTC", "24:00", false},
		{"UTC", "00:60", false},
		{"UTC", "00:00:00", false},
		{"UTC", "+1:00", false},
		{"UTC", "１２:３４", false},
	} {
		t.Run(tc.zone+"/"+tc.at, func(t *testing.T) {
			if got := ValidSiteCheckinSchedule(tc.zone, tc.at); got != tc.valid {
				t.Fatalf("valid=%v, want %v", got, tc.valid)
			}
		})
	}
}
