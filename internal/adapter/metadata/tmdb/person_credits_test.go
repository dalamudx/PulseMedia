package tmdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const personCreditsSample = `{"id":525,"cast":[{"id":7,"media_type":"movie","title":"演员电影","original_title":"Actor film","release_date":""},{"id":7,"media_type":"tv","name":"演员剧集","original_name":"Actor series","first_air_date":"2026-10-01"}],"crew":[{"id":8,"media_type":"movie","title":"导演电影","release_date":"2026-11-01","job":"Director","department":"Directing"},{"id":8,"media_type":"tv","name":"导演剧集","first_air_date":"","job":"Director","department":"Directing"},{"id":9,"media_type":"movie","title":"美术指导","job":"Art Director","department":"Art"},{"id":10,"media_type":"movie","title":"助理导演","job":"First Assistant Director","department":"Directing"}]}`

func TestPersonCreditsNativeRelationshipsTLS(t *testing.T) {
	for _, bearer := range []bool{false, true} {
		t.Run(fmt.Sprint(bearer), func(t *testing.T) {
			var calls atomic.Int32
			key := "owned-key"
			if bearer {
				key = "ey" + strings.Repeat("x", 70)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/person/525/combined_credits" || r.URL.Query().Get("language") != "zh-CN" || r.Header.Get("If-None-Match") != "" {
					t.Error("wrong person credit request")
				}
				if bearer {
					if r.Header.Get("Authorization") != "Bearer "+key || len(r.URL.Query()) != 1 {
						t.Error("incorrect bearer auth")
					}
				} else if r.URL.Query().Get("api_key") != key || len(r.URL.Query()) != 2 {
					t.Error("incorrect API key auth")
				}
				_, _ = io.WriteString(w, personCreditsSample)
			}))
			defer server.Close()
			c := NewClient(key, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
			for _, role := range []string{"actor", "director"} {
				got, err := c.GetPersonCredits(t.Context(), 525, role)
				expectedID := int64(7)
				if role == "director" {
					expectedID = 8
				}
				if err != nil || got.PersonID != 525 || got.Role != role || len(got.Works) != 2 || got.Works[0].TMDBID != expectedID || got.Works[1].TMDBID != expectedID || got.Works[0].MediaType != "movie" || got.Works[1].MediaType != "tv" {
					t.Fatalf("unproven relationship: %+v %v", got, err)
				}
				if role == "actor" && got.Works[0].ReleaseDate != "" || role == "director" && got.Works[1].ReleaseDate != "" {
					t.Fatal("unknown release date fabricated")
				}
			}
			if calls.Load() != 2 {
				t.Fatal("unexpected retry or cache")
			}
		})
	}
}

func TestPersonCreditsRejectMalformedEvidence(t *testing.T) {
	movie := `{"id":7,"media_type":"movie","title":"电影","release_date":"2026-10-01"}`
	cases := []string{
		`null`, `[]`, `{}`, `{"id":526,"cast":[],"crew":[]}`, `{"id":525,"cast":null,"crew":[]}`, `{"id":525,"cast":[]}`, `{"id":525,"cast":[],"crew":null}`,
		`{"id":"525","cast":[],"crew":[]}`, `{"id":525,"id":526,"cast":[],"crew":[]}`, `{"ID":525,"cast":[],"crew":[]}`, `{"id":525,"Id":525,"cast":[],"crew":[]}`,
		`{"id":525,"cast":[],"crew":[],"total_pages":2}`, `{"id":525,"cast":[],"crew":[]} {}`,
		`{"id":525,"cast":[{"id":7,"id":8,"media_type":"movie","title":"电影"}],"crew":[]}`,
		`{"id":525,"cast":[{"ID":7,"media_type":"movie","title":"电影"}],"crew":[]}`,
		`{"id":525,"cast":[{"id":7,"media_type":"movie","Title":"电影"}],"crew":[]}`,
		`{"id":525,"cast":[{"id":0,"media_type":"movie","title":"电影"}],"crew":[]}`,
		`{"id":525,"cast":[{"id":9223372036854775808,"media_type":"movie","title":"电影"}],"crew":[]}`,
		`{"id":525,"cast":[{"id":7,"media_type":"person","title":"电影"}],"crew":[]}`,
		`{"id":525,"cast":[{"id":7,"media_type":"tv","title":"电影"}],"crew":[]}`,
		`{"id":525,"cast":[{"id":7,"media_type":"movie","title":" "}],"crew":[]}`,
		`{"id":525,"cast":[{"id":7,"media_type":"movie","title":"电影","release_date":"2026-02-30"}],"crew":[]}`,
		`{"id":525,"cast":[],"crew":[{"id":7,"media_type":"movie","title":"电影","job":"Director","department":"Art"}]}`,
		`{"id":525,"cast":[` + movie + `,` + strings.Replace(movie, "2026-10-01", "2026-10-02", 1) + `],"crew":[]}`,
		`{"id":525,"cast":[],"crew":[],"x":"` + string([]byte{255}) + `"}`,
	}
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got, err := decodePersonCredits([]byte(raw), 525, "actor")
			if !errors.Is(err, ports.ErrBadResponse) || !reflect.DeepEqual(got, ports.PersonCredits{}) {
				t.Fatalf("accepted unproven evidence: %+v %v", got, err)
			}
		})
	}
}

func TestPersonCreditsDedupeEmptyAndBounds(t *testing.T) {
	raw := `{"id":525,"cast":[{"id":7,"media_type":"movie","title":"电影","character":"A"},{"id":7,"media_type":"movie","title":"电影","character":"B"}],"crew":[]}`
	got, err := decodePersonCredits([]byte(raw), 525, "actor")
	if err != nil || len(got.Works) != 1 {
		t.Fatal("multiple character credits duplicated a work", err)
	}
	for _, role := range []string{"actor", "director"} {
		got, err = decodePersonCredits([]byte(`{"id":525,"cast":[],"crew":[]}`), 525, role)
		if err != nil || got.Works == nil || len(got.Works) != 0 {
			t.Fatal("valid empty credits misclassified", err)
		}
	}
	row := `{"id":7,"media_type":"movie","title":"电影"}`
	for _, raw := range []string{`{"id":525,"cast":[` + strings.Repeat(row+",", 10000) + row + `],"crew":[]}`, strings.Repeat(" ", 5<<20) + `{}`} {
		if got, err := decodePersonCredits([]byte(raw), 525, "actor"); !errors.Is(err, ports.ErrBadResponse) || !reflect.DeepEqual(got, ports.PersonCredits{}) {
			t.Fatal("oversized credits accepted", err)
		}
	}
}

func TestPersonCreditsHTTPFailureBoundaries(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusNotModified, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/redirect")
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "owned private body")
			}))
			defer server.Close()
			got, err := NewClient("owned-private-key", WithBaseURL(server.URL), WithHTTPClient(server.Client())).GetPersonCredits(t.Context(), 525, "actor")
			if err == nil || !reflect.DeepEqual(got, ports.PersonCredits{}) || calls.Load() != 1 || strings.Contains(err.Error(), "owned") || strings.Contains(err.Error(), server.URL) {
				t.Fatal("failure became empty success, retry or leaked data", err)
			}
			switch status {
			case http.StatusUnauthorized, http.StatusForbidden:
				if !errors.Is(err, ports.ErrAuthRequired) {
					t.Fatal(err)
				}
			case http.StatusNotFound:
				if !errors.Is(err, ports.ErrNotFound) {
					t.Fatal(err)
				}
			case http.StatusServiceUnavailable:
				if !errors.Is(err, ports.ErrUnavailable) {
					t.Fatal(err)
				}
			case http.StatusTooManyRequests:
				limited, ok := ports.IsRateLimited(err)
				if !ok || limited.RetryAfter != 17*time.Second {
					t.Fatal(err)
				}
			default:
				if !errors.Is(err, ports.ErrBadResponse) {
					t.Fatal(err)
				}
			}
		})
	}
}

type personCreditsTransport func(*http.Request) (*http.Response, error)

func (f personCreditsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type personCreditsBody struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	reader  *strings.Reader
	closed  atomic.Bool
}

func (b *personCreditsBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.reader.Read(p)
}
func (b *personCreditsBody) Close() error { b.closed.Store(true); return nil }

func TestPersonCreditsCancellationWaitsForIO(t *testing.T) {
	body := &personCreditsBody{started: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(personCreditsSample)}
	var release sync.Once
	defer release.Do(func() { close(body.release) })
	c := NewClient("owned", WithHTTPClient(&http.Client{Transport: personCreditsTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.GetPersonCredits(ctx, 525, "actor"); done <- err }()
	select {
	case <-body.started:
	case <-time.After(2 * time.Second):
		t.Fatal("read did not start")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("returned before physical body read ended")
	default:
	}
	release.Do(func() { close(body.release) })
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !body.closed.Load() {
			t.Fatal("cancellation or body closure lost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not finish")
	}
}

func TestPersonCreditsValidationBeforeIO(t *testing.T) {
	var calls atomic.Int32
	c := NewClient("owned", WithHTTPClient(&http.Client{Transport: personCreditsTransport(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, errors.New("must not send") })}))
	for _, input := range []struct {
		id   int64
		role string
	}{{0, "actor"}, {-1, "director"}, {525, "producer"}, {525, "Director"}, {525, ""}} {
		if _, err := c.GetPersonCredits(t.Context(), input.id, input.role); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("invalid identity or role accepted", err)
		}
	}
	c.apiKey = ""
	if _, err := c.GetPersonCredits(t.Context(), 525, "actor"); !errors.Is(err, ports.ErrAuthRequired) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.GetPersonCredits(ctx, 525, "actor"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid inputs made network requests")
	}
}
