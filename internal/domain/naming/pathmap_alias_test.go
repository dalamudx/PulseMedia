package naming

import "testing"

func TestRemoteMappingRootAndCanonicalAliases(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mapping      map[string]string
		remote, want string
	}{
		{"root", map[string]string{"/": "/local"}, "/movie.mkv", "/local/movie.mkv"},
		{"root itself", map[string]string{"/": "/local"}, "/", "/local"},
		{"specific before root", map[string]string{"/": "/local", "/downloads": "/movies"}, "/downloads/movie.mkv", "/movies/movie.mkv"},
		{"root sibling", map[string]string{"/": "/local", "/downloads": "/movies"}, "/downloads2/movie.mkv", "/local/downloads2/movie.mkv"},
		{"same alias", map[string]string{"/downloads": "/movies", "/downloads/": "/movies"}, "/downloads/movie.mkv", "/movies/movie.mkv"},
		{"conflicting alias", map[string]string{"/downloads": "/movies", "/downloads/": "/other"}, "/downloads/movie.mkv", ""},
		{"bad source", map[string]string{"relative": "/local"}, "/movie.mkv", ""},
		{"bad unrelated destination", map[string]string{"/downloads": "/local", "/other": "relative"}, "/downloads/movie.mkv", ""},
		{"nul remote", nil, "/movie\x00.mkv", ""},
		{"nul source", map[string]string{"/down\x00loads": "/local"}, "/movie.mkv", ""},
		{"nul destination", map[string]string{"/downloads": "/local\x00"}, "/downloads/movie.mkv", ""},
		{"no map", nil, "/movie.mkv", "/movie.mkv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 20; i++ {
				if got := TranslateRemotePath(tc.mapping, tc.remote); got != tc.want {
					t.Fatalf("%q want %q", got, tc.want)
				}
			}
		})
	}
}
