package metadata

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type detailProviderStub struct {
	credits  *ports.MetadataCredits
	season   *ports.MetadataSeasonDetails
	err      error
	calls    int
	kind     string
	id       int64
	number   int
	language string
	block    func()
}

var _ ports.MetadataDetailBrowser = (*detailProviderStub)(nil)

func (p *detailProviderStub) GetMediaCredits(_ context.Context, kind string, id int64, language string) (*ports.MetadataCredits, error) {
	p.calls++
	p.kind, p.id, p.language = kind, id, language
	if p.block != nil {
		p.block()
	}
	return p.credits, p.err
}

func (p *detailProviderStub) GetTVSeasonDetails(_ context.Context, id int64, number int, language string) (*ports.MetadataSeasonDetails, error) {
	p.calls++
	p.id, p.number, p.language = id, number, language
	if p.block != nil {
		p.block()
	}
	return p.season, p.err
}

func detailActor() ports.Actor {
	return ports.Actor{Type: ports.ActorTypeUser, ID: "6b3b2c08-2e4b-4d1d-8bc0-b6fbd22fcfb7", Role: ports.RoleMember}
}

func detailFixture() *detailProviderStub {
	return &detailProviderStub{
		credits: &ports.MetadataCredits{TMDBID: 42, MediaType: "movie", Cast: []ports.MetadataCastMember{
			{TMDBID: 7, Name: "演员", Character: "角色", ProfileURL: "https://image.tmdb.org/t/p/w185/profile.jpg"},
		}},
		season: &ports.MetadataSeasonDetails{TMDBID: 42, SeasonNumber: 2, Complete: true, Episodes: []ports.MetadataEpisodeDetails{
			{TMDBID: 101, EpisodeNumber: 25, Name: "第25集", Overview: "第一行\n第二行", AirDate: "2024-02-29", StillURL: "https://image.tmdb.org/t/p/w300/still.png"},
			{TMDBID: 103, EpisodeNumber: 27},
		}},
	}
}

type detailRead struct {
	name string
	run  func(context.Context, *DetailService, ports.Actor) (bool, error)
}

func detailReads() []detailRead {
	return []detailRead{
		{"credits", func(ctx context.Context, s *DetailService, actor ports.Actor) (bool, error) {
			got, err := s.Credits(ctx, actor, "movie", 42)
			return got != nil, err
		}},
		{"season", func(ctx context.Context, s *DetailService, actor ports.Actor) (bool, error) {
			got, err := s.Season(ctx, actor, 42, 2)
			return got != nil, err
		}},
	}
}

func TestDetailActorsAndScopes(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*ports.Actor)
		allowed bool
	}{
		{"member session", func(*ports.Actor) {}, true},
		{"admin session", func(a *ports.Actor) { a.Role = ports.RoleAdmin }, true},
		{"read token", func(a *ports.Actor) { a.Scopes = []string{"read"} }, true},
		{"admin read token", func(a *ports.Actor) { a.Role = ports.RoleAdmin; a.Scopes = []string{"write", "read"} }, true},
		{"anonymous", func(a *ports.Actor) { *a = ports.Actor{} }, false},
		{"missing id", func(a *ports.Actor) { a.ID = "" }, false},
		{"malformed id", func(a *ports.Actor) { a.ID = "not-a-user-id" }, false},
		{"nil uuid", func(a *ports.Actor) { a.ID = "00000000-0000-0000-0000-000000000000" }, false},
		{"service", func(a *ports.Actor) { a.Type = ports.ActorTypeService; a.Role = ports.RoleAdmin }, false},
		{"system", func(a *ports.Actor) { a.Type = ports.ActorTypeSystem; a.Role = ports.RoleAdmin }, false},
		{"unknown type", func(a *ports.Actor) { a.Type = "other" }, false},
		{"unknown role", func(a *ports.Actor) { a.Role = "owner"; a.Scopes = []string{"read"} }, false},
		{"missing role", func(a *ports.Actor) { a.Role = "" }, false},
		{"empty scopes", func(a *ports.Actor) { a.Scopes = []string{} }, false},
		{"write only", func(a *ports.Actor) { a.Scopes = []string{"write"} }, false},
		{"wildcard", func(a *ports.Actor) { a.Scopes = []string{"*"} }, false},
		{"case sensitive", func(a *ports.Actor) { a.Scopes = []string{"READ"} }, false},
		{"scope whitespace", func(a *ports.Actor) { a.Scopes = []string{" read "} }, false},
	}
	for _, read := range detailReads() {
		for _, tc := range cases {
			t.Run(read.name+"/"+tc.name, func(t *testing.T) {
				actor, p := detailActor(), detailFixture()
				tc.change(&actor)
				present, err := read.run(context.Background(), NewDetailService(p), actor)
				if tc.allowed {
					if err != nil || !present || p.calls != 1 {
						t.Fatalf("allowed read: present=%v err=%v calls=%d", present, err, p.calls)
					}
				} else if err != ports.ErrForbidden || present || p.calls != 0 {
					t.Fatalf("forbidden read: present=%v err=%v calls=%d", present, err, p.calls)
				}
			})
		}
	}
}

func TestDetailInvalidInputHasZeroIO(t *testing.T) {
	for _, kind := range []string{"", "multi", "all", "person", "Movie", " tv "} {
		t.Run("type/"+kind, func(t *testing.T) {
			p := detailFixture()
			got, err := NewDetailService(p).Credits(context.Background(), detailActor(), kind, 42)
			if got != nil || err != ports.ErrRejected || p.calls != 0 {
				t.Fatalf("got=%v err=%v calls=%d", got, err, p.calls)
			}
		})
	}
	for _, id := range []int64{0, -1} {
		for _, read := range []detailRead{
			{"credits", func(ctx context.Context, s *DetailService, a ports.Actor) (bool, error) {
				v, e := s.Credits(ctx, a, "movie", id)
				return v != nil, e
			}},
			{"season", func(ctx context.Context, s *DetailService, a ports.Actor) (bool, error) {
				v, e := s.Season(ctx, a, id, 2)
				return v != nil, e
			}},
		} {
			t.Run(fmt.Sprintf("%s/id%d", read.name, id), func(t *testing.T) {
				p := detailFixture()
				present, err := read.run(context.Background(), NewDetailService(p), detailActor())
				if present || err != ports.ErrRejected || p.calls != 0 {
					t.Fatalf("present=%v err=%v calls=%d", present, err, p.calls)
				}
			})
		}
	}
	p := detailFixture()
	if got, err := NewDetailService(p).Season(context.Background(), detailActor(), 42, -1); got != nil || err != ports.ErrRejected || p.calls != 0 {
		t.Fatalf("negative season: got=%v err=%v calls=%d", got, err, p.calls)
	}
}

func TestDetailProviderArgumentsAndDetachedCopies(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		p := detailFixture()
		p.credits.MediaType = kind
		got, err := NewDetailService(p).Credits(context.Background(), detailActor(), kind, 42)
		if err != nil || !reflect.DeepEqual(got, p.credits) || p.kind != kind || p.id != 42 || p.language != "zh-CN" || p.calls != 1 {
			t.Fatalf("credits request/result: got=%v err=%v provider=%+v", got, err, p)
		}
		p.credits.TMDBID = 999
		p.credits.Cast[0].Name = "changed"
		if got.TMDBID != 42 || got.Cast[0].Name != "演员" {
			t.Fatal("provider mutated accepted credits")
		}
		got.Cast[0].Character = "consumer change"
		if p.credits.Cast[0].Character != "角色" {
			t.Fatal("consumer mutated provider credits")
		}
	}
	for _, number := range []int{0, 2} {
		p := detailFixture()
		p.season.SeasonNumber = number
		got, err := NewDetailService(p).Season(context.Background(), detailActor(), 42, number)
		if err != nil || !reflect.DeepEqual(got, p.season) || p.id != 42 || p.number != number || p.language != "zh-CN" || p.calls != 1 {
			t.Fatalf("season request/result: got=%v err=%v provider=%+v", got, err, p)
		}
		if got.Episodes[0].EpisodeNumber != 25 || got.Episodes[1].EpisodeNumber != 27 || got.Episodes[1].Name != "" || got.Episodes[1].AirDate != "" {
			t.Fatal("sparse numbers or unknown values were synthesized")
		}
		p.season.SeasonNumber = 999
		p.season.Episodes[0].Name = "changed"
		if got.SeasonNumber != number || got.Episodes[0].Name != "第25集" {
			t.Fatal("provider mutated accepted season")
		}
		got.Episodes[0].Overview = "consumer change"
		if p.season.Episodes[0].Overview != "第一行\n第二行" {
			t.Fatal("consumer mutated provider season")
		}
	}
}

func TestDetailCompleteEmptyArraysSucceed(t *testing.T) {
	p := detailFixture()
	p.credits.Cast = []ports.MetadataCastMember{}
	p.season.Episodes = []ports.MetadataEpisodeDetails{}
	s := NewDetailService(p)
	credits, err := s.Credits(context.Background(), detailActor(), "movie", 42)
	if err != nil || credits == nil || credits.Cast == nil || len(credits.Cast) != 0 {
		t.Fatalf("empty cast: %v %v", credits, err)
	}
	season, err := s.Season(context.Background(), detailActor(), 42, 2)
	if err != nil || season == nil || !season.Complete || season.Episodes == nil || len(season.Episodes) != 0 {
		t.Fatalf("empty season: %v %v", season, err)
	}
}

func TestDetailMalformedCreditsFailClosed(t *testing.T) {
	cases := map[string]func(*detailProviderStub){
		"nil response": func(p *detailProviderStub) { p.credits = nil },
		"foreign id":   func(p *detailProviderStub) { p.credits.TMDBID++ },
		"foreign type": func(p *detailProviderStub) { p.credits.MediaType = "tv" },
		"missing type": func(p *detailProviderStub) { p.credits.MediaType = "" },
		"nil cast":     func(p *detailProviderStub) { p.credits.Cast = nil },
		"cast too long": func(p *detailProviderStub) {
			p.credits.Cast = make([]ports.MetadataCastMember, 25)
			for i := range p.credits.Cast {
				p.credits.Cast[i] = ports.MetadataCastMember{TMDBID: int64(i + 1), Name: "Actor"}
			}
		},
		"zero person id":     func(p *detailProviderStub) { p.credits.Cast[0].TMDBID = 0 },
		"negative person id": func(p *detailProviderStub) { p.credits.Cast[0].TMDBID = -1 },
		"empty name":         func(p *detailProviderStub) { p.credits.Cast[0].Name = "" },
		"blank name":         func(p *detailProviderStub) { p.credits.Cast[0].Name = " \t" },
		"invalid utf8":       func(p *detailProviderStub) { p.credits.Cast[0].Name = string([]byte{0xff}) },
		"control name":       func(p *detailProviderStub) { p.credits.Cast[0].Name = "Actor\x00" },
		"secret name":        func(p *detailProviderStub) { p.credits.Cast[0].Name = "api_key=private" },
		"secret character":   func(p *detailProviderStub) { p.credits.Cast[0].Character = "Bearer private" },
		"control character":  func(p *detailProviderStub) { p.credits.Cast[0].Character = "role\n" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := detailFixture()
			change(p)
			got, err := NewDetailService(p).Credits(context.Background(), detailActor(), "movie", 42)
			if got != nil || err != ports.ErrBadResponse || p.calls != 1 {
				t.Fatalf("got=%v err=%v calls=%d", got, err, p.calls)
			}
		})
	}
	p := detailFixture()
	p.credits.Cast = make([]ports.MetadataCastMember, 24)
	for i := range p.credits.Cast {
		p.credits.Cast[i] = ports.MetadataCastMember{TMDBID: int64(i + 1), Name: "Actor"}
	}
	if got, err := NewDetailService(p).Credits(context.Background(), detailActor(), "movie", 42); err != nil || got == nil || len(got.Cast) != 24 {
		t.Fatalf("24-member boundary: %v %v", got, err)
	}
}

func TestDetailMalformedSeasonsFailClosed(t *testing.T) {
	cases := map[string]func(*detailProviderStub){
		"nil response":        func(p *detailProviderStub) { p.season = nil },
		"foreign show":        func(p *detailProviderStub) { p.season.TMDBID++ },
		"foreign season":      func(p *detailProviderStub) { p.season.SeasonNumber++ },
		"incomplete":          func(p *detailProviderStub) { p.season.Complete = false },
		"nil episodes":        func(p *detailProviderStub) { p.season.Episodes = nil },
		"zero episode id":     func(p *detailProviderStub) { p.season.Episodes[0].TMDBID = 0 },
		"negative episode id": func(p *detailProviderStub) { p.season.Episodes[0].TMDBID = -1 },
		"duplicate id":        func(p *detailProviderStub) { p.season.Episodes[1].TMDBID = p.season.Episodes[0].TMDBID },
		"zero number":         func(p *detailProviderStub) { p.season.Episodes[0].EpisodeNumber = 0 },
		"negative number":     func(p *detailProviderStub) { p.season.Episodes[0].EpisodeNumber = -1 },
		"duplicate number":    func(p *detailProviderStub) { p.season.Episodes[1].EpisodeNumber = 25 },
		"impossible date":     func(p *detailProviderStub) { p.season.Episodes[0].AirDate = "2023-02-29" },
		"noncanonical date":   func(p *detailProviderStub) { p.season.Episodes[0].AirDate = "2024-2-09" },
		"timestamp":           func(p *detailProviderStub) { p.season.Episodes[0].AirDate = "2024-02-29T00:00:00Z" },
		"ancient date":        func(p *detailProviderStub) { p.season.Episodes[0].AirDate = "0000-01-01" },
		"invalid utf8":        func(p *detailProviderStub) { p.season.Episodes[0].Name = string([]byte{0xff}) },
		"unsafe name":         func(p *detailProviderStub) { p.season.Episodes[0].Name = "cookie: private" },
		"unsafe overview":     func(p *detailProviderStub) { p.season.Episodes[0].Overview = "https://secret.test" },
		"control overview":    func(p *detailProviderStub) { p.season.Episodes[0].Overview = "text\x00" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := detailFixture()
			change(p)
			got, err := NewDetailService(p).Season(context.Background(), detailActor(), 42, 2)
			if got != nil || err != ports.ErrBadResponse || p.calls != 1 {
				t.Fatalf("got=%v err=%v calls=%d", got, err, p.calls)
			}
		})
	}
}

func TestDetailImagesUseFixedSafeProjections(t *testing.T) {
	for _, read := range detailReads() {
		for _, raw := range []string{
			"", "https://image.tmdb.org/t/p/SIZE/safe-file_1.webp",
			"https://image.tmdb.org/t/p/w500/safe-file_1.webp",
			"http://image.tmdb.org/t/p/SIZE/file.jpg", "https://image.tmdb.org.evil.test/t/p/SIZE/file.jpg",
			"https://image.tmdb.org:443/t/p/SIZE/file.jpg", "https://user" + "@" + "image.tmdb.org/t/p/SIZE/file.jpg",
			"https://image.tmdb.org/t/p/original/file.jpg", "https://image.tmdb.org/t/p/w342/file.jpg",
			"https://image.tmdb.org/t/p/SIZE/file.jpg?api_key=secret", "https://image.tmdb.org/t/p/SIZE/file.jpg?",
			"https://image.tmdb.org/t/p/SIZE/file.jpg#fragment", "https://image.tmdb.org/t/p/SIZE/%66ile.jpg",
			"https://image.tmdb.org/t/p/SIZE/../file.jpg", "https://image.tmdb.org/t/p/SIZE/nested/file.jpg",
			"https://image.tmdb.org/t/p/SIZE/file.svg", "https://image.tmdb.org/t/p/SIZE/file\\escape.jpg",
		} {
			size := "w185"
			if read.name == "season" {
				size = "w300"
			}
			raw = strings.ReplaceAll(raw, "SIZE", size)
			t.Run(read.name+"/"+raw, func(t *testing.T) {
				p := detailFixture()
				p.credits.Cast[0].ProfileURL, p.season.Episodes[0].StillURL = raw, raw
				present, err := read.run(context.Background(), NewDetailService(p), detailActor())
				valid := raw == "" || raw == "https://image.tmdb.org/t/p/"+size+"/safe-file_1.webp" || raw == "https://image.tmdb.org/t/p/w500/safe-file_1.webp"
				if valid && (err != nil || !present) {
					t.Fatalf("valid projection: %v %v", present, err)
				}
				if !valid && (err != ports.ErrBadResponse || present) {
					t.Fatalf("unsafe projection: %v %v", present, err)
				}
			})
		}
	}
}

func TestDetailSafeProviderErrors(t *testing.T) {
	for _, read := range detailReads() {
		for _, class := range []error{ports.ErrNotFound, ports.ErrUnavailable, ports.ErrBadResponse, context.Canceled, context.DeadlineExceeded} {
			t.Run(read.name+"/"+class.Error(), func(t *testing.T) {
				p := detailFixture()
				p.err = fmt.Errorf("api_key=secret: %w", class)
				present, err := read.run(context.Background(), NewDetailService(p), detailActor())
				if present || err != class {
					t.Fatalf("classification/leak: present=%v err=%v", present, err)
				}
			})
		}
		for _, raw := range []error{errors.New("https://secret.test/?passkey=private"), ports.ErrAuthRequired, ports.ErrTimeout, ports.ErrBlocked, ports.ErrRejected} {
			p := detailFixture()
			p.err = raw
			present, err := read.run(context.Background(), NewDetailService(p), detailActor())
			if present || err != ports.ErrUnavailable {
				t.Fatalf("raw error: present=%v err=%v", present, err)
			}
		}
		p := detailFixture()
		original := ports.NewRateLimitError(17*time.Second, "api_key=secret")
		p.err = fmt.Errorf("secret transport: %w", original)
		present, err := read.run(context.Background(), NewDetailService(p), detailActor())
		limited, ok := ports.IsRateLimited(err)
		if present || !ok || limited.RetryAfter != 17*time.Second || limited.Reason != "" || strings.Contains(err.Error(), "secret") {
			t.Fatalf("rate limit lost or leaked: %v %v", present, err)
		}
		limited.Reason = "consumer change"
		if raw, _ := ports.IsRateLimited(original); raw.Reason != "api_key=secret" {
			t.Fatal("rate limit shares provider buffer")
		}
		present, err = read.run(context.Background(), NewDetailService(nil), detailActor())
		if present || err != ports.ErrUnavailable {
			t.Fatalf("missing provider: %v %v", present, err)
		}
	}
}

func TestDetailCanceledBeforeProviderHasZeroIO(t *testing.T) {
	for _, read := range detailReads() {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%v", read.name, deadline), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				if deadline {
					cancel()
					ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
				} else {
					cancel()
				}
				defer cancel()
				p := detailFixture()
				present, err := read.run(ctx, NewDetailService(p), detailActor())
				if present || err != ctx.Err() || p.calls != 0 {
					t.Fatalf("pre-canceled: %v %v calls=%d", present, err, p.calls)
				}
			})
		}
	}
}

func TestDetailCancellationWaitsForIgnoringProvider(t *testing.T) {
	for _, read := range detailReads() {
		for _, providerErr := range []error{nil, errors.New("api_key=secret")} {
			t.Run(fmt.Sprintf("%s/error=%v", read.name, providerErr != nil), func(t *testing.T) {
				entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				p := detailFixture()
				p.err = providerErr
				p.block = func() { close(entered); <-release }
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					defer close(returned)
					present, err := read.run(ctx, NewDetailService(p), detailActor())
					if present {
						result <- errors.New("canceled read returned success")
					} else {
						result <- err
					}
				}()
				t.Cleanup(func() {
					unblock()
					select {
					case <-returned:
					case <-time.After(3 * time.Second):
						t.Error("read goroutine did not finish")
					}
				})
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("provider was not called")
				}
				cancel()
				select {
				case err := <-result:
					t.Fatalf("released before physical I/O returned: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
				unblock()
				select {
				case err := <-result:
					if err != context.Canceled {
						t.Fatalf("late provider outcome replaced cancellation: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("canceled read did not finish after provider returned")
				}
			})
		}
	}
}
