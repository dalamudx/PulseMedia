package media_test

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Compare only explicit independent upstream field labels. Unknown stays unknown;
// this partial attribute safety oracle is not full golden acceptance.
func TestMITKnownResolutionMatchesIndependentField(t *testing.T) {
	checked := 0
	for _, entry := range readMITCorpora(t) {
		raw, ok := entry.Expected["screen_size"]
		if !ok {
			continue
		}
		var expected string
		if json.Unmarshal(raw, &expected) != nil {
			continue
		}
		checked++
		p := media.RecognizeRelease(entry.Name).Quality
		if p.Resolution != "" && string(p.Resolution) != expected {
			t.Errorf("known resolution contradicted independent label %q: got %s source %s", entry.Name, p.Resolution, expected)
		}
	}
	if checked < 50 {
		t.Fatal("independent labelled resolution coverage disappeared", checked)
	}
}
