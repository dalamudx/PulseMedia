package naming

import "testing"

func TestRemoteMappingBoundaryLongestAndTraversal(t *testing.T) {
	mapping := map[string]string{"/downloads": "/local/base", "/downloads/movies": "/local/movies"}
	for i := 0; i < 20; i++ {
		if got := TranslateRemotePath(mapping, "/downloads/movies/movie.mkv"); got != "/local/movies/movie.mkv" {
			t.Fatal(got)
		}
	}
	for remote, want := range map[string]string{"/downloads2/movie.mkv": "/downloads2/movie.mkv", "/downloads/../private.mkv": "", "relative.mkv": "", "/downloads": "/local/base"} {
		if got := TranslateRemotePath(mapping, remote); got != want {
			t.Fatalf("%q -> %q want %q", remote, got, want)
		}
	}
	if got := TranslateRemotePath(map[string]string{"/downloads": "relative"}, "/downloads/movie.mkv"); got != "" {
		t.Fatal(got)
	}
}
