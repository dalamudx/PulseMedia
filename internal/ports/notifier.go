package ports

import (
	"context"
	"time"
)

type Notification struct {
	EventID   string    `json:"event_id"`
	Topic     string    `json:"topic"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Severity  string    `json:"severity"`
	Timestamp time.Time `json:"timestamp"`
}

// Notifier abstracts external notification destinations.
type Notifier interface {
	Send(ctx context.Context, n Notification) error
	Test(ctx context.Context) error
}
