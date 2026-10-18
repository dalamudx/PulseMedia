package qbittorrent

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
	"testing"
)

func TestQBFileInventoryExactProgress(t *testing.T) {
	for _, c := range []struct {
		raw     string
		want    float64
		invalid bool
	}{
		{`0.999999999999999999999`, -1, false}, {`0.12345678901234568`, 0.12345678901234568, false}, {`1`, 1, false}, {`1e0`, 1, false}, {`0`, 0, false},
		{`1.00000000000000000001`, 0, true}, {`-1e-1000000000`, 0, true}, {`1e1000000000`, 0, true}, {`"1"`, 0, true}, {`null`, 0, true},
	} {
		t.Run(c.raw, func(t *testing.T) {
			body := fmt.Sprintf(`[{"index":0,"name":"Show.S01E01.mkv","size":9007199254740993,"progress":%s}]`, c.raw)
			client := observationClient(t, body, nil)
			files, e := client.GetTorrentFiles(context.Background(), strings.Repeat("a", 40))
			if c.invalid {
				if !errors.Is(e, ports.ErrBadResponse) || len(files) != 0 {
					t.Fatalf("invalid progress accepted: %v", e)
				}
				return
			}
			if e != nil || len(files) != 1 || files[0].SizeBytes != 9007199254740993 {
				t.Fatalf("valid inventory failed: %v", e)
			}
			if c.want == -1 {
				if files[0].Progress >= 1 || files[0].Progress <= 0 {
					t.Fatal("incomplete inventory certified complete")
				}
			} else if files[0].Progress != c.want {
				t.Fatal("inventory precision changed")
			}
		})
	}
}
