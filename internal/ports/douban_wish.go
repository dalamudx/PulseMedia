package ports

import (
	"context"
	"errors"
)

const DoubanWishPageSize = 15
const MaxDoubanWishStart = 15000

// This is an upstream access failure, not the current PulseMedia actor's role.
var ErrDoubanWishAccessDenied = errors.New("douban wish access denied")

// Titles are provider-displayed aliases, not proven TMDB identities. Year=0
// remains unknown. MediaType must come from an explicitly selected page scope.
type DoubanWishItem struct {
	DoubanID  string
	Titles    []string
	Year      int
	MediaType string
}

// A page proves only its observed window, never a consistent multi-page snapshot.
// ProfileNumber is optional; a numeric requested ID must match it or CanonicalID.
type DoubanWishPage struct {
	RequestedID   string
	CanonicalID   string
	ProfileNumber string
	MediaType     string
	Start         int
	// WindowEnd is the declared exclusive offset, not Start+len(Items).
	// Douban may omit unavailable subjects from a declared fifteen-item window.
	WindowEnd      int
	WindowComplete bool
	Total          int
	NextStart      *int
	Items          []DoubanWishItem
}

type DoubanWishProvider interface {
	ReadWishPage(context.Context, string, string, int) (DoubanWishPage, error)
}
