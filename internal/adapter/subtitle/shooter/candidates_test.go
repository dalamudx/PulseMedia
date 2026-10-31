package shooter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCandidateProtocolBoundedAndValidatesUnselectedLinks(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		t.Run(fmt.Sprint(unsafe), func(t *testing.T) {
			var gets atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					gets.Add(1)
					_, _ = io.WriteString(w, chineseSRT)
					return
				}
				files := []any{}
				for i := 0; i < 12; i++ {
					link := "http://" + r.Host + fmt.Sprintf("/subtitle/%d?signature=private", i)
					if unsafe && i == 11 {
						link = "https://untrusted.invalid/private"
					}
					files = append(files, map[string]string{"Ext": "srt", "Link": link})
				}
				_ = json.NewEncoder(w).Encode([]any{map[string]any{"Files": files}})
			}))
			defer s.Close()
			got, e := NewClient(s.URL, s.Client()).FindSubtitleCandidates(context.Background(), videoHash())
			if unsafe {
				if !errors.Is(e, ports.ErrBadResponse) || gets.Load() != 0 {
					t.Fatal("unsafe unselected link was used", e, gets.Load())
				}
			} else if e != nil || len(got) != 8 || gets.Load() != 8 {
				t.Fatal("candidate bounds", len(got), gets.Load(), e)
			}
		})
	}
}
func TestCandidateNoResultAndErrorsRemainDistinct(t *testing.T) {
	for _, status := range []int{200, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status); _, _ = w.Write([]byte{0xff}) }))
			defer s.Close()
			got, e := NewClient(s.URL, s.Client()).FindSubtitleCandidates(context.Background(), videoHash())
			if status == 200 {
				if e != nil || len(got) != 0 {
					t.Fatal(got, e)
				}
			} else if e == nil {
				t.Fatal("upstream error became empty result")
			}
		})
	}
}
