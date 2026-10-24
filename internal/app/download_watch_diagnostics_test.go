package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

// Store only the controller's fixed reason code, never complete log content.
func (p *movieWatchProbe) Write(raw []byte) (int, error) {
	var event struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &event) == nil && event.Reason != "" && len(event.Reason) < 128 && !strings.ContainsAny(event.Reason, "/:?@") {
		p.lastReason.Store(event.Reason)
	}
	return len(raw), nil
}

func watchReadinessDiagnostics(t *testing.T, ctx context.Context, f *organizationFixture, sources []ports.DownloadWatchSource) {
	t.Helper()
	var save, content string
	_ = f.pool.QueryRow(ctx, `SELECT save_path,coalesce(content_path,'') FROM download_task LIMIT 1`).Scan(&save, &content)
	t.Logf("watch path shape save_clean=%t content_empty=%t content_clean=%t", filepath.Clean(save) == save, content == "", filepath.Clean(content) == content)
	for _, source := range sources {
		page, e := postgres.NewDownloadWatchStore(f.pool).ResolveDownloadWatchTargets(ctx, source.Binding, ports.RootChangeHint{RelativePath: ".", Kind: ports.RootChangeRescan}, ports.DownloadWatchTargetQuery{})
		t.Logf("watch source kind=%s valid=%t target_count=%d resolution_failed=%t", source.Binding.SourceKind, validDownloadWatchBinding(source.Binding), len(page.Targets), e != nil)
	}
}
