package transfer

import "testing"

func TestCompleteTVExplicitPathMapping(t *testing.T) {
	cases := []struct {
		name         string
		mapping      map[string]string
		remote, want string
	}{
		{"mapped", map[string]string{"/downloads": "/allowed"}, "/downloads/Show/E01.mkv", "/allowed/Show/E01.mkv"},
		{"identity", map[string]string{"/allowed": "/allowed"}, "/allowed/Show/E01.mkv", "/allowed/Show/E01.mkv"},
		{"longest", map[string]string{"/downloads": "/allowed", "/downloads/Show": "/other"}, "/downloads/Show/E01.mkv", "/other/E01.mkv"},
		{"filesystem-root", map[string]string{"/": "/allowed"}, "/Show/E01.mkv", "/allowed/Show/E01.mkv"},
		{"sibling-prefix", map[string]string{"/downloads": "/allowed"}, "/downloads2/Show/E01.mkv", ""},
		{"unmapped-local", map[string]string{"/downloads": "/allowed"}, "/allowed/Show/E01.mkv", ""},
		{"empty", nil, "/allowed/Show/E01.mkv", ""},
		{"escape", map[string]string{"/downloads": "/allowed"}, "/downloads/../Show/E01.mkv", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, e := completeTVMappedSource(c.mapping, c.remote)
			if got != c.want || (e != nil) != (c.want == "") {
				t.Fatal(got, e)
			}
		})
	}
}
