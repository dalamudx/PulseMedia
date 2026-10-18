package httpapi_test

import (
	"os"
	"testing"
)

func TestSelectedMovieAuthorizationBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("owned browser fixture required")
	}
	t.Setenv("PULSE_SELECTED_USER_BROWSER", "1")
	t.Setenv("PULSE_SELECTED_UI_FIXTURE", "1")
	t.Setenv("PULSE_SELECTED_CALENDAR_BROWSER", "1")
	TestSelectedMovieUIFixture(t)
}
