package library

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// UntrackedReviewService records review and confirmation intent, never publication.
type UntrackedReviewService struct {
	repo  ports.UntrackedReviewRepository
	files ports.RootFilesystem
}

func NewUntrackedReviewService(repo ports.UntrackedReviewRepository, files ports.RootFilesystem) *UntrackedReviewService {
	return &UntrackedReviewService{repo: repo, files: files}
}

func untrackedReviewError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, safe := range []error{ports.ErrUntrackedReviewStale, ports.ErrRejected, ports.ErrForbidden, ports.ErrUnauthorized, ports.ErrNotFound, ports.ErrEndpointVersionConflict} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ports.ErrUnavailable
}

func untrackedReviewActor(ctx context.Context, actor *ports.Actor, scopeID, fileID uuid.UUID, key string) (ports.Actor, error) {
	a, err := untrackedScopeActor(ctx, actor, scopeID)
	if err != nil {
		return ports.Actor{}, err
	}
	if fileID == uuid.Nil || !untrackedScopeKey(key) {
		return ports.Actor{}, ports.ErrRejected
	}
	return a, nil
}

func untrackedReviewVersion(v int64) bool { return v > 0 && v < math.MaxInt64 }
func untrackedReviewInputValid(in ports.UntrackedReviewInput) bool {
	return untrackedReviewVersion(in.ScopeVersion) && untrackedReviewVersion(in.FileRevision) && (in.MediaType == "movie" || in.MediaType == "tv") && in.TMDBID >= 0 && in.CanonicalSelectionValid()
}
func untrackedReviewTime(t time.Time) bool { return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999 }
func untrackedReviewRelative(p string) bool {
	if len(p) > 4096 || p == "." || !utf8.ValidString(p) || !untrackedDiscoveryRelative(p) {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func untrackedReviewHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func untrackedReviewCandidateKey(key string) bool {
	parts := strings.Split(key, ":")
	if len(parts) != 2 || (parts[0] != "tmdb.movie" && parts[0] != "tmdb.tv") {
		return false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == parts[1]
}
func untrackedReviewConfirmationInput(in ports.UntrackedConfirmationInput) bool {
	if in.ReviewID == uuid.Nil || !untrackedReviewHash(in.MaterialHash) || !untrackedReviewVersion(in.ScopeVersion) || !untrackedReviewVersion(in.FileRevision) || !untrackedReviewCandidateKey(in.CandidateKey) || !untrackedReviewRelative(in.TargetRelativePath) || !naming.IsVideoPath(in.TargetRelativePath) {
		return false
	}
	switch in.Action {
	case "hardlink", "copy", "reflink", "move":
		return true
	}
	return false
}
func untrackedReviewRequestValid(r ports.UntrackedReviewRequest, actor string, scopeID, fileID uuid.UUID) bool {
	return r.ID != uuid.Nil && r.JobID != uuid.Nil && r.ActorID != uuid.Nil && r.ActorID.String() == actor && r.ScopeID == scopeID && r.FileID == fileID && untrackedReviewInputValid(r.Input) && untrackedReviewTime(r.CreatedAt)
}
func untrackedReviewRequestReceiptValid(r *ports.UntrackedReviewRequestReceipt, actor string, scopeID, fileID uuid.UUID, key string) bool {
	return r != nil && !r.ExecutionAllowed && r.CommandKey == key && untrackedReviewRequestValid(r.Request, actor, scopeID, fileID)
}

func (s *UntrackedReviewService) Request(ctx context.Context, actor *ports.Actor, scopeID, fileID uuid.UUID, input ports.UntrackedReviewInput, key string) (*ports.UntrackedReviewRequestReceipt, error) {
	a, err := untrackedReviewActor(ctx, actor, scopeID, fileID, key)
	if err != nil {
		return nil, err
	}
	if !untrackedReviewInputValid(input) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	r, err := s.repo.RequestUntrackedReview(ctx, &a, scopeID, fileID, input, key)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedReviewError(ctx, err)
	}
	if !untrackedReviewRequestReceiptValid(r, owner, scopeID, fileID, key) || r.Request.Input != input {
		return nil, ports.ErrUnavailable
	}
	out := *r
	return &out, nil
}

func (s *UntrackedReviewService) RequestReceipt(ctx context.Context, actor *ports.Actor, scopeID, fileID uuid.UUID, key string) (*ports.UntrackedReviewRequestReceipt, error) {
	a, err := untrackedReviewActor(ctx, actor, scopeID, fileID, key)
	if err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	r, err := s.repo.ReadUntrackedReviewRequestReceipt(ctx, &a, scopeID, fileID, key)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedReviewError(ctx, err)
	}
	if !untrackedReviewRequestReceiptValid(r, owner, scopeID, fileID, key) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	return &out, nil
}

func untrackedReviewDate(t *time.Time) bool {
	if t == nil {
		return true // Unknown air dates remain unknown; they do not grant acquisition eligibility.
	}
	date := t.UTC()
	return date.Year() >= 1800 && date.Year() <= 9999 && date.Hour() == 0 && date.Minute() == 0 && date.Second() == 0 && date.Nanosecond() == 0
}
func untrackedReviewCandidateValid(c *ports.UntrackedReviewCandidate, kind string) bool {
	if c == nil || c.TMDBID < 1 || c.Namespace != "tmdb."+kind || c.Key != c.Namespace+":"+strconv.FormatInt(c.TMDBID, 10) || media.WatchlistTitleKey(c.Title) == "" || (c.OriginalTitle != "" && media.WatchlistTitleKey(c.OriginalTitle) == "") || c.Year < 1800 || c.Year > 9999 || len(c.Units) == 0 || len(c.Units) > 128 {
		return false
	}
	if kind == "movie" {
		return len(c.Units) == 1 && c.Units[0] == (ports.UntrackedReviewUnit{UnitKey: "movie"})
	}
	if kind != "tv" {
		return false
	}
	seen, ids := map[string]bool{}, map[int64]bool{}
	season, previous := c.Units[0].Season, 0
	for _, u := range c.Units {
		if u.Season < 1 || u.Season > 99 || u.Season != season || u.Episode <= previous || u.Episode > 9999 || u.UnitKey != media.FormatEpisodeUnitKey(u.Season, u.Episode) || u.TMDBEpisodeID < 1 || seen[u.UnitKey] || ids[u.TMDBEpisodeID] || !untrackedReviewDate(u.AirDate) {
			return false
		}
		previous, seen[u.UnitKey], ids[u.TMDBEpisodeID] = u.Episode, true, true
	}
	return true
}
func untrackedReviewEvidenceValid(e *ports.UntrackedReviewEvidence, in ports.UntrackedReviewInput) bool {
	if e == nil || e.ParserVersion == "" || len(e.ParserVersion) > 160 || e.Selection != untrackedReviewSelection(in) || (e.ParsedTitle != "" && media.WatchlistTitleKey(e.ParsedTitle) == "") || (e.ParsedYear != 0 && (e.ParsedYear < 1800 || e.ParsedYear > 9999)) {
		return false
	}
	if !e.Complete {
		return e.Candidate == nil && untrackedReviewReason(e.Reason) && e.Reason != "ready"
	}
	if e.Reason != "ready" || !untrackedReviewCandidateValid(e.Candidate, in.MediaType) || !in.CanonicalCandidateMatches(e.Candidate) {
		return false
	}
	if in.TMDBID > 0 {
		return in.TMDBID == e.Candidate.TMDBID
	}
	return e.ParsedYear == e.Candidate.Year && untrackedReviewTitleMatches(e.ParsedTitle, e.Candidate.Title, e.Candidate.OriginalTitle)
}
func untrackedReviewValid(r *ports.UntrackedReview, actor string, scopeID, fileID, reviewID uuid.UUID) bool {
	if r == nil || r.ExecutionAllowed || r.Request.ID != reviewID || !untrackedReviewRequestValid(r.Request, actor, scopeID, fileID) {
		return false
	}
	switch r.State {
	case "queued", "running", "dead":
		return r.Evidence == nil && r.MaterialHash == "" && r.RecordedAt == nil
	case "ready", "review_required":
		return untrackedReviewHash(r.MaterialHash) && r.RecordedAt != nil && untrackedReviewTime(*r.RecordedAt) && !r.RecordedAt.Before(r.Request.CreatedAt) && untrackedReviewEvidenceValid(r.Evidence, r.Request.Input) && (r.State == "ready") == r.Evidence.Complete
	}
	return false
}

func (s *UntrackedReviewService) Read(ctx context.Context, actor *ports.Actor, scopeID, fileID, reviewID uuid.UUID) (*ports.UntrackedReview, error) {
	a, err := untrackedReviewActor(ctx, actor, scopeID, fileID, "read")
	if err != nil {
		return nil, err
	}
	if reviewID == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	r, err := s.repo.ReadUntrackedReview(ctx, &a, scopeID, fileID, reviewID)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedReviewError(ctx, err)
	}
	if !untrackedReviewValid(r, owner, scopeID, fileID, reviewID) {
		return nil, ports.ErrUnavailable
	}
	out := untrackedReviewCopy(*r)
	return &out, nil
}

func untrackedReviewScopeValid(scope ports.UntrackedScope, scopeID uuid.UUID) bool {
	return untrackedScopeValid(scope, scopeID) && untrackedReviewVersion(scope.Version) && untrackedReviewTime(scope.ClientVersion) && untrackedReviewTime(scope.CreatedAt) && untrackedReviewTime(scope.UpdatedAt) && utf8.ValidString(scope.ClientStamp) && utf8.ValidString(scope.LocalRoot) && utf8.ValidString(scope.TargetRootPath) && utf8.ValidString(scope.RemotePrefix) && untrackedReviewRelative(scope.TargetMarker)
}
func untrackedReviewFileValid(f ports.UntrackedDiscoveredFile, scope ports.UntrackedScope, fileID uuid.UUID) bool {
	return f.ID == fileID && fileID != uuid.Nil && f.ScopeID == scope.ID && f.ScopeVersion == scope.Version && untrackedReviewVersion(f.Revision) && untrackedReviewRelative(f.RelativePath) && naming.IsVideoPath(f.RelativePath) && untrackedScopeIdentity(f.Identity) && f.Identity.Size >= 0 && f.Observations > 0 && untrackedReviewTime(f.FirstObservedAt) && untrackedReviewTime(f.LastObservedAt) && !f.LastObservedAt.Before(f.FirstObservedAt)
}
func untrackedReviewMaterialValid(m ports.UntrackedConfirmationMaterial, actor string, scopeID, fileID uuid.UUID, in ports.UntrackedConfirmationInput) bool {
	return m.ID != uuid.Nil && m.ActorID.String() == actor && m.Input == in && untrackedReviewConfirmationInput(in) && untrackedReviewTime(m.CreatedAt) && !m.CreatedAt.Before(m.Review.Request.CreatedAt) && m.Scope.Enabled && m.Scope.OwnerID.String() == actor && untrackedReviewScopeValid(m.Scope, scopeID) && m.Scope.Version == in.ScopeVersion && m.Scope.TargetMediaType == m.Review.Request.Input.MediaType && untrackedReviewFileValid(m.File, m.Scope, fileID) && m.File.Revision == in.FileRevision && untrackedReviewValid(&m.Review, actor, scopeID, fileID, in.ReviewID) && !m.Review.Stale && m.Review.State == "ready" && m.Review.MaterialHash == in.MaterialHash && m.Review.Request.Input.ScopeVersion == in.ScopeVersion && m.Review.Request.Input.FileRevision == in.FileRevision && m.Review.Evidence.Candidate.Key == in.CandidateKey && !m.CreatedAt.Before(*m.Review.RecordedAt) && !m.CreatedAt.Before(m.Scope.UpdatedAt) && !m.CreatedAt.Before(m.File.LastObservedAt)
}
func untrackedReviewConfirmationReceiptValid(r *ports.UntrackedConfirmationReceipt, actor string, scopeID, fileID uuid.UUID, key string) bool {
	return r != nil && !r.ExecutionAllowed && r.State == "confirmed_pending_organization" && r.CommandKey == key && untrackedReviewMaterialValid(r.Material, actor, scopeID, fileID, r.Material.Input)
}

// Normalize instants only for comparison; historical receipt bytes remain untouched.
func untrackedReviewInstant(t time.Time) time.Time { return t.Round(0).UTC() }
func untrackedReviewCopy(r ports.UntrackedReview) ports.UntrackedReview {
	if r.RecordedAt != nil {
		t := *r.RecordedAt
		r.RecordedAt = &t
	}
	if r.Evidence != nil {
		e := *r.Evidence
		r.Evidence = &e
		if e.Candidate != nil {
			c := *e.Candidate
			e.Candidate = &c
			if c.Units != nil {
				c.Units = append([]ports.UntrackedReviewUnit{}, c.Units...)
			}
			for i := range c.Units {
				if c.Units[i].AirDate != nil {
					t := *c.Units[i].AirDate
					c.Units[i].AirDate = &t
				}
			}
		}
	}
	return r
}
func untrackedReviewMaterialCopy(m ports.UntrackedConfirmationMaterial) ports.UntrackedConfirmationMaterial {
	m.Review = untrackedReviewCopy(m.Review)
	return m
}
func untrackedReviewMaterialNormalized(m ports.UntrackedConfirmationMaterial) ports.UntrackedConfirmationMaterial {
	m = untrackedReviewMaterialCopy(m)
	m.CreatedAt = untrackedReviewInstant(m.CreatedAt)
	m.Scope.ClientVersion = untrackedReviewInstant(m.Scope.ClientVersion)
	m.Scope.CreatedAt = untrackedReviewInstant(m.Scope.CreatedAt)
	m.Scope.UpdatedAt = untrackedReviewInstant(m.Scope.UpdatedAt)
	m.File.FirstObservedAt = untrackedReviewInstant(m.File.FirstObservedAt)
	m.File.LastObservedAt = untrackedReviewInstant(m.File.LastObservedAt)
	m.Review.Request.CreatedAt = untrackedReviewInstant(m.Review.Request.CreatedAt)
	if m.Review.RecordedAt != nil {
		*m.Review.RecordedAt = untrackedReviewInstant(*m.Review.RecordedAt)
	}
	if m.Review.Evidence != nil && m.Review.Evidence.Candidate != nil {
		for i := range m.Review.Evidence.Candidate.Units {
			p := m.Review.Evidence.Candidate.Units[i].AirDate
			if p != nil {
				*p = untrackedReviewInstant(*p)
			}
		}
	}
	return m
}
func untrackedReviewMaterialEqual(a, b ports.UntrackedConfirmationMaterial) bool {
	return reflect.DeepEqual(untrackedReviewMaterialNormalized(a), untrackedReviewMaterialNormalized(b))
}

func (s *UntrackedReviewService) Confirm(ctx context.Context, actor *ports.Actor, scopeID, fileID uuid.UUID, input ports.UntrackedConfirmationInput, key string) (*ports.UntrackedConfirmationReceipt, error) {
	a, err := untrackedReviewActor(ctx, actor, scopeID, fileID, key)
	if err != nil {
		return nil, err
	}
	if !untrackedReviewConfirmationInput(input) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	p, err := s.repo.PrepareUntrackedConfirmation(ctx, &a, scopeID, fileID, input, key)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedReviewError(ctx, err)
	}
	if p == nil || (p.Receipt == nil) == (p.Capture == nil) {
		return nil, ports.ErrUnavailable
	}
	if p.Receipt != nil {
		if !untrackedReviewConfirmationReceiptValid(p.Receipt, owner, scopeID, fileID, key) || p.Receipt.Material.Input != input {
			return nil, ports.ErrUnavailable
		}
		out := *p.Receipt
		out.Material = untrackedReviewMaterialCopy(out.Material)
		return &out, nil
	}
	m := untrackedReviewMaterialCopy(p.Capture.UntrackedConfirmationMaterial())
	if !untrackedReviewMaterialValid(m, owner, scopeID, fileID, input) {
		return nil, ports.ErrUnavailable
	}
	physical, err := untrackedReviewOpen(ctx, s.files, m.Scope, m.File)
	if err != nil {
		return nil, err
	}
	defer physical.close()
	if err := physical.check(ctx); err != nil {
		return nil, err
	}
	if !untrackedReviewMaterialEqual(m, p.Capture.UntrackedConfirmationMaterial()) {
		return nil, ports.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := s.repo.CommitUntrackedConfirmation(ctx, p.Capture)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedReviewError(ctx, err)
	}
	if !untrackedReviewConfirmationReceiptValid(r, owner, scopeID, fileID, key) || !untrackedReviewMaterialEqual(m, r.Material) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	out.Material = untrackedReviewMaterialCopy(out.Material)
	return &out, nil
}

func (s *UntrackedReviewService) ConfirmationReceipt(ctx context.Context, actor *ports.Actor, scopeID, fileID uuid.UUID, key string) (*ports.UntrackedConfirmationReceipt, error) {
	a, err := untrackedReviewActor(ctx, actor, scopeID, fileID, key)
	if err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	r, err := s.repo.ReadUntrackedConfirmationReceipt(ctx, &a, scopeID, fileID, key)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedReviewError(ctx, err)
	}
	if !untrackedReviewConfirmationReceiptValid(r, owner, scopeID, fileID, key) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	out.Material = untrackedReviewMaterialCopy(out.Material)
	return &out, nil
}

// LockRoots owns physical ordering and device/inode deduplication, as in discovery.
// Independent capabilities and namespace reopens prevent trusting a displaced FD.
type untrackedReviewPhysical struct {
	files          ports.RootFilesystem
	scope          ports.UntrackedScope
	file           ports.UntrackedDiscoveredFile
	source, target ports.TransferRoot
	locked         map[string]ports.TransferRoot
	release        func()
	marker         ports.FileIdentity
}

func (p *untrackedReviewPhysical) close() {
	if p.source != nil {
		p.source.Close()
	}
	if p.target != nil {
		p.target.Close()
	}
	if p.release != nil {
		p.release()
	}
}
func untrackedReviewOpen(ctx context.Context, files ports.RootFilesystem, scope ports.UntrackedScope, file ports.UntrackedDiscoveredFile) (_ *untrackedReviewPhysical, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if files == nil {
		return nil, ports.ErrUnavailable
	}
	p := &untrackedReviewPhysical{files: files, scope: scope, file: file}
	defer func() {
		if err != nil {
			p.close()
		}
	}()
	p.source, err = files.OpenRoot(ctx, scope.LocalRoot)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || p.source == nil {
		return nil, ports.ErrUnavailable
	}
	if err = untrackedReviewRootIdentity(ctx, p.source, scope.SourceIdentity); err != nil {
		return nil, err
	}
	p.target, err = files.OpenRoot(ctx, scope.TargetRootPath)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || p.target == nil {
		return nil, ports.ErrUnavailable
	}
	if err = untrackedReviewRootIdentity(ctx, p.target, scope.TargetIdentity); err != nil {
		return nil, err
	}
	p.locked, p.release, err = files.LockRoots(ctx, scope.LocalRoot, scope.TargetRootPath)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || p.release == nil || p.locked[scope.LocalRoot] == nil || p.locked[scope.TargetRootPath] == nil {
		return nil, ports.ErrUnavailable
	}
	p.marker, err = p.target.Inspect(ctx, scope.TargetMarker)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || !untrackedScopeIdentity(p.marker) || p.marker.Size < 0 {
		return nil, ports.ErrUntrackedReviewStale
	}
	if err = p.check(ctx); err != nil {
		return nil, err
	}
	return p, nil
}
func untrackedReviewRootIdentity(ctx context.Context, root ports.TransferRoot, expected ports.FileIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i, err := root.Identity(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || untrackedScopePhysical(i) != untrackedScopePhysical(expected) {
		return ports.ErrUntrackedReviewStale
	}
	return nil
}
func (p *untrackedReviewPhysical) root(ctx context.Context, root ports.TransferRoot, expected ports.FileIdentity, source bool) error {
	if err := untrackedReviewRootIdentity(ctx, root, expected); err != nil {
		return err
	}
	relative, identity := p.scope.TargetMarker, p.marker
	if source {
		relative, identity = p.file.RelativePath, p.file.Identity
	}
	current, err := root.Inspect(ctx, relative)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || current != identity {
		return ports.ErrUntrackedReviewStale
	}
	return nil
}
func (p *untrackedReviewPhysical) check(ctx context.Context) error {
	for _, item := range []struct {
		path     string
		expected ports.FileIdentity
		held     ports.TransferRoot
		source   bool
	}{
		{p.scope.LocalRoot, p.scope.SourceIdentity, p.source, true},
		{p.scope.TargetRootPath, p.scope.TargetIdentity, p.target, false},
	} {
		if err := p.root(ctx, item.held, item.expected, item.source); err != nil {
			return err
		}
		if err := p.root(ctx, p.locked[item.path], item.expected, item.source); err != nil {
			return err
		}
		err := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			root, err := p.files.OpenRoot(ctx, item.path)
			if root != nil {
				defer root.Close()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil || root == nil {
				return ports.ErrUntrackedReviewStale
			}
			return p.root(ctx, root, item.expected, item.source)
		}()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}
