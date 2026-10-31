package download

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
)

type submissionPreparer struct {
	attempt *ports.PreparedDownload
	err     error
}

func (p submissionPreparer) PrepareDownload(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, ...ports.JobLease) (*ports.PreparedDownload, error) {
	return p.attempt, p.err
}

type submissionClient struct {
	ports.DownloadClient
	add func(context.Context, ports.AddTorrentParam) error
}

func (c submissionClient) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	return c.add(ctx, p)
}

func TestSubmissionUsecaseKeepsBudgetThroughPhysicalCallAndCompletion(t *testing.T) {
	for _, outcome := range []string{"success", "unknown", "completion-failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			task := &ports.DownloadExecutionTask{ID: uuid.New(), State: "submitting"}
			sendErr := errors.New("response lost")
			completeErr := errors.New("lease expired")
			released, completed := false, false
			adds := 0
			params := ports.AddTorrentParam{InfoHash: "original-hash", SavePath: "/original", Tags: "original-tag"}
			attempt := &ports.PreparedDownload{Task: task, Params: params, Release: func() {
				if !completed {
					t.Error("budget released before completion")
				}
				released = true
			}}
			attempt.Client = submissionClient{add: func(c context.Context, p ports.AddTorrentParam) error {
				adds++
				if released || c != ctx || p.InfoHash != params.InfoHash || p.SavePath != params.SavePath || p.Tags != params.Tags {
					t.Error("frozen send changed")
				}
				if outcome == "cancelled" {
					cancel()
					if released {
						t.Error("logical cancellation released physical budget")
					}
					return context.Canceled
				}
				if outcome == "unknown" {
					return sendErr
				}
				return nil
			}}
			attempt.Complete = func(c context.Context, e error) (*ports.DownloadExecutionTask, error) {
				if released || c != ctx {
					t.Error("completion lost budget/context")
				}
				completed = true
				if outcome == "unknown" && !errors.Is(e, sendErr) {
					t.Error("lost uncertain result")
				}
				if outcome == "completion-failed" {
					return task, completeErr
				}
				return task, e
			}
			got, err := SubmitDownload(ctx, submissionPreparer{attempt: attempt}, uuid.New(), uuid.New(), uuid.New(), "/original")
			if got != task || adds != 1 || !completed || !released {
				t.Fatal("lifecycle incomplete")
			}
			switch outcome {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if !errors.Is(err, sendErr) {
					t.Fatal(err)
				}
			case "completion-failed":
				if !errors.Is(err, completeErr) {
					t.Fatal(err)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSubmissionUsecaseReplayAndRejectedPreparationNeverSend(t *testing.T) {
	for _, state := range []string{"unknown", "submitting", "tracking", "completed"} {
		task := &ports.DownloadExecutionTask{ID: uuid.New(), State: state}
		got, err := SubmitDownload(t.Context(), submissionPreparer{attempt: &ports.PreparedDownload{Task: task}}, uuid.New(), uuid.New(), uuid.New(), "/different")
		if err != nil || got != task {
			t.Fatalf("replay %s: %v", state, err)
		}
	}
	rejected := errors.New("rejected")
	called := false
	attempt := &ports.PreparedDownload{Client: submissionClient{add: func(context.Context, ports.AddTorrentParam) error { called = true; return nil }}, Release: func() {}}
	if _, err := SubmitDownload(t.Context(), submissionPreparer{attempt: attempt, err: rejected}, uuid.New(), uuid.New(), uuid.New(), "/"); !errors.Is(err, rejected) || called {
		t.Fatal("rejected preparation sent")
	}
}

func TestSubmissionUsecaseRejectsIncompletePreparation(t *testing.T) {
	for _, attempt := range []*ports.PreparedDownload{nil, {}, {Task: &ports.DownloadExecutionTask{}, Complete: func(context.Context, error) (*ports.DownloadExecutionTask, error) {
		t.Fatal("unexpected completion")
		return nil, nil
	}}, {Task: &ports.DownloadExecutionTask{}, Client: submissionClient{add: func(context.Context, ports.AddTorrentParam) error { t.Fatal("unexpected Add"); return nil }}}} {
		_, err := SubmitDownload(t.Context(), submissionPreparer{attempt: attempt}, uuid.New(), uuid.New(), uuid.New(), "/")
		if !errors.Is(err, ports.ErrUnavailable) {
			t.Fatalf("incomplete preparation accepted: %v", err)
		}
	}
}
