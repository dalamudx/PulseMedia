package app

import "testing"

func TestAppRunCompleteMovieDirectory(t *testing.T) {
	for _, mode := range []string{"", "root"} {
		t.Run("sidecars_"+mode, func(t *testing.T) { runFullMoviePipelineSource(t, true, mode, "sidecars") })
	}
}

func TestM8MovieDirectoryFiltersSampleRuntimePG(t *testing.T) {
	runFullMoviePipelineSource(t, true, "", "filtered_sample")
}
