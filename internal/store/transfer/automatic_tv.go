package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type automaticTVOrganizationInputs struct {
	Authority      ports.AutomaticTVExecutionAuthority
	Proof          automaticTVOrganizationProof
	Original       db.AutomaticTVOrganizationOriginalRow
	Task           db.DownloadTask
	Root           db.LibraryRoot
	Client         db.DownloadClient
	ClientSnapshot []byte
	Candidate      db.ReleaseCandidate
	Job            db.Job
	Source         string
}

// Any surviving automatic provenance owns dispatch, even when its other proof
// has gone missing. Historic schemas may lack all automatic tables.
func automaticTVOrganizationPresent(ctx context.Context, q *db.Queries, operation uuid.UUID) (bool, error) {
	schema, e := q.AutomaticTVOrganizationSchema(ctx)
	if e != nil {
		return false, e
	}
	for _, value := range []any{schema.Authority, schema.Preparation, schema.Organization} {
		if _, ok := value.(bool); !ok {
			return true, ErrReviewRequired
		}
	}
	if schema.Authority == true {
		if _, e = q.AutomaticTVSubmissionAuthority(ctx, operation); e == nil {
			return true, nil
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return true, e
		}
	}
	if schema.Preparation == true {
		if _, e = q.AutomaticTVSubmissionPreparation(ctx, operation); e == nil {
			return true, nil
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return true, e
		}
	}
	if schema.Organization == true {
		if _, e = q.AutomaticTVOrganizationPlan(ctx, operation); e == nil {
			return true, nil
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return true, e
		}
	}
	demands, e := q.ListRequirementsForOperation(ctx, operation)
	if e != nil {
		return false, e
	}
	for _, d := range demands {
		if len(d.SubmissionFence) == 0 {
			continue
		}
		var f struct {
			Kind      string    `json:"kind"`
			Authority uuid.UUID `json:"authority"`
		}
		if json.Unmarshal(d.SubmissionFence, &f) != nil {
			return true, ErrReviewRequired
		}
		if f.Kind == "automatic_tv_episode_v1" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) hasAutomaticTVOrganization(ctx context.Context, operation uuid.UUID) (bool, error) {
	return automaticTVOrganizationPresent(ctx, s.queries, operation)
}

func readAutomaticTVOrganization(ctx context.Context, q *db.Queries, operation uuid.UUID) (*automaticTVOrganizationInputs, error) {
	r, e := q.AutomaticTVOrganizationOriginal(ctx, operation)
	if e != nil {
		return nil, automaticTVOrganizationReadError(e)
	}
	a, e := automaticTVOrganizationAuthority(r.Authority, operation)
	if e != nil {
		return nil, e
	}
	proof, e := automaticTVOrganizationVideo(r.Proof, a, r.Inventory)
	if e != nil {
		return nil, e
	}
	task, e := q.GetDownloadTaskByOpID(ctx, operation)
	if e != nil {
		return nil, e
	}
	if task.State != "completed" || task.ClientID != a.Target.ClientID || task.CandidateID != a.CandidateID || task.InfoHash != proof.InfoHash || task.SavePath != proof.SavePath || task.Progress != 1 || task.TotalBytes != proof.TotalBytes || task.DownloadedBytes != proof.TotalBytes {
		return nil, ErrReviewRequired
	}
	client, e := q.GetDownloadClientByID(ctx, task.ClientID)
	if e != nil {
		return nil, e
	}
	var mapping map[string]string
	if json.Unmarshal(client.RemotePathMap, &mapping) != nil {
		return nil, ErrReviewRequired
	}
	source, e := automaticTVOrganizationSource(mapping, proof)
	if e != nil {
		return nil, e
	}
	return &automaticTVOrganizationInputs{Authority: a, Proof: proof, Original: r, Task: task, Client: client, Source: source}, nil
}

func automaticTVOrganizationReadError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrReviewRequired
	}
	return e
}

// This order is shared with the original decision/send/observation boundaries.
// It checks current permission without applying the original five-minute Add TTL.
func lockAutomaticTVOrganizationCurrent(ctx context.Context, q *db.Queries, operation uuid.UUID) (*automaticTVOrganizationInputs, error) {
	in, e := readAutomaticTVOrganization(ctx, q, operation)
	if e != nil {
		return nil, e
	}
	a := in.Authority
	t := a.Target
	owner, e := q.LockAcquisitionCommandUser(ctx, t.OwnerID)
	if e != nil {
		return nil, e
	}
	if _, e = q.LockJobForWrite(ctx, in.Original.OrganizationJobID); e != nil {
		return nil, e
	}
	in.Job, e = q.GetJobByID(ctx, in.Original.OrganizationJobID)
	if e != nil {
		return nil, e
	}
	policy, e := q.LockSearchPolicy(ctx, t.PolicyID)
	if e != nil {
		return nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, t.SubscriptionID)
	if e != nil {
		return nil, e
	}
	root, e := q.LockAcquisitionRoot(ctx, t.RootID)
	if e != nil {
		return nil, e
	}
	in.Root = root
	unit, e := q.LockAcquisitionUnit(ctx, t.UnitID)
	if e != nil {
		return nil, e
	}
	wanted, e := q.LockSearchWanted(ctx, t.WantedID)
	if e != nil {
		return nil, e
	}
	if e = q.LockDownloadAcquisition(ctx, operation); e != nil {
		return nil, e
	}
	in.Task, e = q.LockDownloadTaskByOperation(ctx, operation)
	if e != nil {
		return nil, e
	}
	parent, e := q.LockSearchMedia(ctx, t.MediaID)
	if e != nil {
		return nil, e
	}
	idx, e := q.LockSearchIndexer(ctx, a.IndexerID)
	if e != nil {
		return nil, e
	}
	in.Candidate, e = q.LockSubmissionCandidate(ctx, a.CandidateID)
	if e != nil {
		return nil, e
	}
	in.Client, e = q.AutomaticTVOrganizationLockClient(ctx, t.ClientID)
	if e != nil {
		return nil, e
	}
	in.ClientSnapshot, e = q.AutomaticTVOrganizationClientSnapshot(ctx, t.ClientID)
	if e != nil {
		return nil, e
	}
	// Locks on the complete own protection set precede all ownership decisions.
	if _, e = q.ListCompleteTVPreparationLanes(ctx, operation); e != nil {
		return nil, e
	}
	if _, e = q.ListCompleteTVPreparationReservations(ctx, operation); e != nil {
		return nil, e
	}
	if _, e = q.AutomaticTVSubmissionRequirements(ctx, operation); e != nil {
		return nil, e
	}
	stamp, e := clientStamp(in.Client)
	if e != nil {
		return nil, e
	}
	if !owner.IsActive || (owner.Role != ports.RoleMember && owner.Role != ports.RoleAdmin) || sub.UserID != t.OwnerID || sub.State != "active" || sub.IntentVersion != t.IntentVersion || sub.MediaID != t.MediaID || sub.RootID != t.RootID || sub.QualityPolicyID != t.PolicyID || sub.QualityPolicyRevision != t.PolicyRevision || policy.CurrentRevision != t.PolicyRevision || sub.UpgradeMode != "disabled" || !sub.DownloaderID.Valid || uuid.UUID(sub.DownloaderID.Bytes) != t.ClientID || sub.EpisodeMappingRevision.Int64 != t.MappingRevision || sub.EpisodeMappingRevision.Valid != (t.MappingRevision > 0) || root.MediaType != "tv" || root.Health != "online" || !root.BaselineCompleted || root.Generation != t.RootGeneration || root.HealthRevision != t.RootHealthRevision || automaticTVOrganizationDigest([]byte(root.Path)) != a.RootPathDigest || unit.MediaID != t.MediaID || unit.MetadataVersion != t.UnitVersion || unit.UnitKey != t.UnitKey || unit.Retired || !unit.SeasonNumber.Valid || unit.SeasonNumber.Int32 != t.Season || !unit.EpisodeNumber.Valid || unit.EpisodeNumber.Int32 != t.Episode || unit.AbsoluteNumber.Valid || !unit.AirDate.Valid || !unit.AirDate.Time.Equal(t.AirDate) || wanted.SubscriptionID != sub.ID || wanted.UnitID != unit.ID || wanted.IsSkipped || wanted.UserSkipped || wanted.ScopeExcluded || wanted.IntentVersion != t.IntentVersion || parent.MediaType != "tv" || parent.MetadataVersion != t.MediaVersion || parent.Title != t.Title || parent.OriginalTitle.String != t.OriginalTitle || !parent.Year.Valid || parent.Year.Int32 != t.Year || stamp != t.ClientStamp || (in.Client.ClientType != "qbittorrent" && in.Client.ClientType != "transmission") {
		return nil, ErrReviewRequired
	}
	if in.Task.ID == uuid.Nil || in.Task.State != "completed" || in.Task.ClientID != t.ClientID || in.Task.CandidateID != a.CandidateID || in.Task.InfoHash != in.Proof.InfoHash || in.Task.SavePath != in.Proof.SavePath || in.Task.TotalBytes != in.Proof.TotalBytes || in.Task.Progress != 1 || in.Task.DownloadedBytes != in.Proof.TotalBytes {
		return nil, ErrReviewRequired
	}
	revision, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: t.PolicyID, Revision: t.PolicyRevision})
	if e != nil {
		return nil, e
	}
	if !sameAutomaticTVJSON(revision.Rules, t.Rules) {
		return nil, ErrReviewRequired
	}
	aired, e := q.AutomaticTVAired(ctx, unit.ID)
	if e != nil {
		return nil, e
	}
	if !aired.Valid || !aired.Bool {
		return nil, ErrReviewRequired
	}
	identities, e := q.ListSearchMediaIdentities(ctx, parent.ID)
	if e != nil {
		return nil, e
	}
	identity := false
	for _, id := range identities {
		identity = identity || (id.Namespace == "tmdb.tv" && id.ExternalID == strconv.FormatInt(t.TMDBID, 10))
	}
	episode, e := q.GetTMDBEpisodeIdentityForUnit(ctx, unit.ID)
	if e != nil {
		return nil, automaticTVOrganizationReadError(e)
	}
	if !identity || episode.ExternalID != t.EpisodeID || episode.TmdbSeriesID != t.TMDBID {
		return nil, ErrReviewRequired
	}
	if t.MappingRevision > 0 {
		mapping, e := q.GetCurrentEpisodeMapping(ctx, sub.ID)
		if e != nil {
			return nil, e
		}
		if mapping.Revision != t.MappingRevision || mapping.Mode != "standard" || mapping.MediaID != parent.ID || mapping.MediaVersion != parent.MetadataVersion {
			return nil, ErrReviewRequired
		}
	}
	var bound []uuid.UUID
	if json.Unmarshal(sub.IndexerIds, &bound) != nil || !slices.Contains(bound, a.IndexerID) || t.IndexerVersions[a.IndexerID.String()] != a.SourceConfigVersion {
		return nil, ErrReviewRequired
	}
	current, e := q.AutomaticTVSourceCurrent(ctx, db.AutomaticTVSourceCurrentParams{ID: idx.ID, ConfigVersion: a.SourceConfigVersion})
	if e != nil {
		return nil, e
	}
	if !current {
		return nil, ErrReviewRequired
	}
	original, e := q.AutomaticTVOrganizationCandidateOriginal(ctx, operation)
	if e != nil {
		return nil, e
	}
	c := in.Candidate
	if !original || c.IndexerID != a.IndexerID || c.SourceConfigVersion != a.SourceConfigVersion || quality.AcquisitionSourceStamp(c.Title, c.InfoHash.String, c.LinkEncrypted, c.QualityProfile) != a.SourceStamp {
		return nil, ErrReviewRequired
	}
	var profile quality.Profile
	var rules quality.PolicyRules
	if json.Unmarshal(c.QualityProfile, &profile) != nil || !quality.KnownProfile(profile) || json.Unmarshal(t.Rules, &rules) != nil || quality.ValidateRules(rules) != nil || !quality.EvaluateCandidate(rules, c.Title, profile).Passed {
		return nil, ErrReviewRequired
	}
	identityDecision := media.DecideAutomaticTVIdentity(c.Title, t.Title, t.OriginalTitle, int(t.Year), t.TMDBID)
	rows, _ := media.ResolveStandardSingleEpisode(media.RecognizeRelease(c.Title), []media.NumberingUnit{{ID: t.UnitID, EpisodeID: t.EpisodeID, Season: int(t.Season), Episode: int(t.Episode), Key: t.UnitKey, Version: t.UnitVersion}})
	if identityDecision.State != "eligible" || len(rows) != 1 || rows[0].UnitID != t.UnitID || rows[0].EpisodeID != t.EpisodeID {
		return nil, ErrReviewRequired
	}
	return in, nil
}

func prelockAutomaticTVTransfer(ctx context.Context, q *db.Queries, operation uuid.UUID) (bool, error) {
	handled, e := automaticTVOrganizationPresent(ctx, q, operation)
	if e != nil || !handled {
		return handled, e
	}
	_, e = lockAutomaticTVOrganizationCurrent(ctx, q, operation)
	return true, e
}

func automaticTVOrganizationProtected(ctx context.Context, q *db.Queries, a ports.AutomaticTVExecutionAuthority) error {
	valid, e := q.AutomaticTVOrganizationProtections(ctx, db.AutomaticTVOrganizationProtectionsParams{OperationID: a.OperationID, Fence: automaticTVOrganizationDemand(a)})
	if e != nil {
		return e
	}
	if !valid {
		return ErrReviewRequired
	}
	return nil
}

func validateAutomaticTVTransfer(ctx context.Context, q *db.Queries, operation uuid.UUID, frozen transferTargetEvidence) error {
	in, e := lockAutomaticTVOrganizationCurrent(ctx, q, operation)
	if e != nil {
		return e
	}
	if !automaticTVOrganizationTargetValid(in.Authority, frozen) || frozen.InventoryTaskID != in.Task.ID || frozen.AutomaticTVInventoryStamp != in.Original.InventoryStamp || frozen.InventorySourcePath != in.Source || frozen.InventorySizeBytes != in.Proof.Video.Size {
		return ErrReviewRequired
	}
	p, e := q.AutomaticTVOrganizationPlan(ctx, operation)
	if e != nil {
		return automaticTVOrganizationReadError(e)
	}
	raw, _ := json.Marshal(frozen)
	if p.TaskID != in.Task.ID || p.OrganizationJobID != in.Job.ID || p.ObservationJobID != in.Original.ObservationJobID || p.InventoryStamp != in.Original.InventoryStamp || p.AuthorityHash != in.Original.AuthorityHash || p.ProofHash != in.Original.ProofHash || !sameAutomaticTVJSON(p.TargetEvidence, raw) || !sameAutomaticTVJSON(p.QualityProfile, in.Candidate.QualityProfile) || p.RootPath != in.Root.Path || p.RootGeneration != in.Root.Generation || p.SourceSize != in.Proof.Video.Size || filepath.Join(p.SourceRoot, p.SourceRel) != in.Source || !in.Task.TransferPlanID.Valid || uuid.UUID(in.Task.TransferPlanID.Bytes) != p.PlanID {
		return ErrReviewRequired
	}
	baseline, e := q.GetTrustedScanRootIdentity(ctx, db.GetTrustedScanRootIdentityParams{RootID: p.RootID, Generation: p.RootGeneration})
	if e != nil {
		return automaticTVOrganizationReadError(e)
	}
	if baseline.RootDevice != p.BaselineDevice || baseline.RootInode != p.BaselineInode || p.RootDevice != p.BaselineDevice || p.RootInode != p.BaselineInode {
		return ErrReviewRequired
	}
	return automaticTVOrganizationProtected(ctx, q, in.Authority)
}

func (s *Service) organizeAutomaticTVEpisode(ctx context.Context, lease ports.JobLease, task db.DownloadTask, client db.DownloadClient) error {
	if task.State != "completed" || task.ClientID != client.ID {
		return ErrReviewRequired
	}
	if task.TransferPlanID.Valid {
		p, e := s.queries.AutomaticTVOrganizationPlan(ctx, task.OperationID)
		if e != nil {
			return automaticTVOrganizationReadError(e)
		}
		if p.PlanID != uuid.UUID(task.TransferPlanID.Bytes) || p.TaskID != task.ID || p.OrganizationJobID != lease.ID {
			return ErrReviewRequired
		}
		if e = s.resumeAutomaticTVOrganization(ctx, lease, p); e != nil {
			return e
		}
		return s.executePlan(ctx, p.PlanID, lease)
	}
	plan, e := s.createAutomaticTVOrganizationPlan(ctx, lease, task.OperationID)
	if e != nil {
		return e
	}
	return s.executePlan(ctx, plan.ID, lease)
}

// A rollback can leave an exact hardlink and a reviewed logical plan. Only the
// original automatic job may resume it, after observing the SAME frozen link
// under physical slots and rechecking every current permission. No Add is sent.
func (s *Service) resumeAutomaticTVOrganization(ctx context.Context, lease ports.JobLease, p db.AutomaticTvOrganizationPlan) error {
	plan, e := s.queries.GetTransferPlanByID(ctx, p.PlanID)
	if e != nil {
		return e
	}
	if plan.State != "review_required" {
		return nil
	}
	steps, e := s.queries.ListStepsForPlan(ctx, p.PlanID)
	if e != nil {
		return e
	}
	if len(steps) != 1 || steps[0].ID != p.StepID {
		return ErrReviewRequired
	}
	step := steps[0]
	roots, release, e := s.files.LockRoots(ctx, p.SourceRoot, p.SourceDirectory, p.RootPath)
	if e != nil {
		return e
	}
	defer release()
	observed, e := s.queries.GetSearchDatabaseTime(ctx)
	if e != nil {
		return e
	}
	for _, item := range []struct {
		path          string
		device, inode int64
	}{{p.SourceRoot, p.SourceRootDevice, p.SourceRootInode}, {p.SourceDirectory, p.SourceDirectoryDevice, p.SourceDirectoryInode}, {p.RootPath, p.RootDevice, p.RootInode}} {
		identity, e := roots[item.path].Identity(ctx)
		if e != nil {
			return e
		}
		if identity.Device != item.device || identity.Inode != item.inode {
			return ErrReviewRequired
		}
	}
	marker, e := roots[p.RootPath].Inspect(ctx, p.MarkerFilename)
	if e != nil {
		return e
	}
	if marker != (ports.FileIdentity{Device: p.MarkerDevice, Inode: p.MarkerInode, Size: p.MarkerSize, Mtime: p.MarkerMtime}) {
		return ErrReviewRequired
	}
	for _, item := range []struct{ root, rel string }{{p.SourceRoot, p.SourceRel}, {p.RootPath, step.TargetRel.String}} {
		identity, e := roots[item.root].Inspect(ctx, item.rel)
		if e != nil {
			return e
		}
		if identity != frozen(step) {
			return ErrReviewRequired
		}
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	in, e := lockAutomaticTVOrganizationCurrent(ctx, q, p.OperationID)
	if e != nil {
		return e
	}
	if in.Job.ID != lease.ID || lease.ID != p.OrganizationJobID {
		return ErrExecutionRejected
	}
	if e = guardOrganization(ctx, q, []ports.JobLease{lease}); e != nil {
		return e
	}
	var f transferTargetEvidence
	if json.Unmarshal(step.TargetEvidence, &f) != nil {
		return ErrReviewRequired
	}
	if e = validateAutomaticTVTransfer(ctx, q, p.OperationID, f); e != nil {
		return e
	}
	latest, e := q.GetTransferPlanByID(ctx, p.PlanID)
	if e != nil {
		return e
	}
	if latest.State != "review_required" || latest.ExecutionOwner != plan.ExecutionOwner || latest.ExecutionToken != plan.ExecutionToken {
		return ErrExecutionRejected
	}
	if _, e = q.UpdatePlanState(ctx, db.UpdatePlanStateParams{ID: p.PlanID, State: "approved"}); e != nil {
		return e
	}
	payload, _ := json.Marshal(map[string]any{"operation_id": p.OperationID, "plan_id": p.PlanID, "task_id": p.TaskID})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: "system", ActorID: in.Authority.RoundID.String(), Action: "automatic_tv.organization_resumed", TargetType: "transfer_plan", TargetID: p.PlanID.String(), Payload: payload}); e != nil {
		return e
	}
	if e = validateAutomaticTVTransfer(ctx, q, p.OperationID, f); e != nil {
		return e
	}
	if e = guardOrganization(ctx, q, []ports.JobLease{lease}); e != nil {
		return e
	}
	fresh, e := q.IsTransferObservationFresh(ctx, observed)
	if e != nil {
		return e
	}
	if !fresh.Valid || !fresh.Bool {
		return ErrReviewRequired
	}
	return tx.Commit(ctx)
}

func (s *Service) createAutomaticTVOrganizationPlan(ctx context.Context, lease ports.JobLease, operation uuid.UUID) (db.TransferPlan, error) {
	var zero db.TransferPlan
	in, e := readAutomaticTVOrganization(ctx, s.queries, operation)
	if e != nil {
		return zero, e
	}
	root, e := s.queries.GetLibraryRootByID(ctx, in.Authority.Target.RootID)
	if e != nil {
		return zero, e
	}
	configured, e := s.queries.ListTransferSourceRoots(ctx, operation)
	if e != nil {
		return zero, e
	}
	sort.Slice(configured, func(i, j int) bool { return len(configured[i]) > len(configured[j]) })
	sourceRoot, sourceRel := "", ""
	for _, path := range configured {
		if rel, err := relative(path, in.Source); err == nil {
			sourceRoot, sourceRel = path, rel
			break
		}
	}
	if sourceRoot == "" {
		return zero, ports.ErrFileEscape
	}
	for _, part := range strings.Split(filepath.ToSlash(sourceRel), "/") {
		if strings.HasPrefix(part, ".") {
			return zero, ports.ErrFileEscape
		}
	}
	sourceDir := filepath.Dir(in.Source)
	roots, release, e := s.files.LockRoots(ctx, sourceRoot, sourceDir, root.Path)
	if e != nil {
		return zero, e
	}
	defer release()
	observed, e := s.queries.GetSearchDatabaseTime(ctx)
	if e != nil {
		return zero, e
	}
	sourceIdentity, e := roots[sourceRoot].Identity(ctx)
	if e != nil {
		return zero, e
	}
	directoryIdentity, e := roots[sourceDir].Identity(ctx)
	if e != nil {
		return zero, e
	}
	video, e := roots[sourceRoot].Inspect(ctx, sourceRel)
	if e != nil {
		return zero, e
	}
	targetIdentity, e := roots[root.Path].Identity(ctx)
	if e != nil {
		return zero, e
	}
	marker := root.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	markerIdentity, e := roots[root.Path].Inspect(ctx, marker)
	if e != nil {
		return zero, e
	}
	if video.Size != in.Proof.Video.Size || video.Device != targetIdentity.Device {
		return zero, ErrReviewRequired
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return zero, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	current, e := lockAutomaticTVOrganizationCurrent(ctx, q, operation)
	if e != nil {
		return zero, e
	}
	if current.Job.ID != lease.ID || current.Task.OrganizationState != "queued" || current.Task.TransferPlanID.Valid || current.Root.Path != root.Path || current.Source != in.Source || !sameAutomaticTVJSON(current.Original.Proof, in.Original.Proof) {
		return zero, ErrReviewRequired
	}
	if e = guardOrganization(ctx, q, []ports.JobLease{lease}); e != nil {
		return zero, e
	}
	if e = automaticTVOrganizationProtected(ctx, q, current.Authority); e != nil {
		return zero, e
	}
	baseline, e := q.GetTrustedScanRootIdentity(ctx, db.GetTrustedScanRootIdentityParams{RootID: root.ID, Generation: root.Generation})
	if e != nil {
		return zero, automaticTVOrganizationReadError(e)
	}
	if baseline.RootDevice != targetIdentity.Device || baseline.RootInode != targetIdentity.Inode {
		return zero, ErrReviewRequired
	}
	targetRel, e := naming.FormatEpisodeRel(current.Authority.Target.Title, int(current.Authority.Target.Year), int(current.Authority.Target.Season), int(current.Authority.Target.Episode), mustAutomaticTVProfile(current.Candidate.QualityProfile), filepath.Ext(current.Source))
	if e != nil {
		return zero, e
	}
	plan, e := q.CreateTransferPlan(ctx, db.CreateTransferPlanParams{ID: s.idgen.NewUUID(), OperationID: operation, RootID: root.ID, State: "approved", Strategy: "hardlink"})
	if e != nil {
		return zero, e
	}
	if e = q.FreezeTransferPlan(ctx, db.FreezeTransferPlanParams{ID: plan.ID, RootPath: text(root.Path), RootGeneration: integer(root.Generation), RootDevice: integer(targetIdentity.Device), RootInode: integer(targetIdentity.Inode)}); e != nil {
		return zero, e
	}
	step, e := q.CreateTransferStep(ctx, db.CreateTransferStepParams{ID: s.idgen.NewUUID(), PlanID: plan.ID, UnitID: current.Authority.Target.UnitID, SourcePath: current.Source, TargetPath: filepath.Join(root.Path, targetRel), Action: "hardlink", StepOrder: 1, SourceSize: video.Size, SourceMtime: video.Mtime})
	if e != nil {
		return zero, e
	}
	evidence, _ := json.Marshal(automaticTVOrganizationTarget(current.Authority, current.Task.ID, current.Original.InventoryStamp, current.Source, video.Size))
	if e = q.FreezeTransferStep(ctx, db.FreezeTransferStepParams{ID: step.ID, SourceRoot: text(sourceRoot), SourceRel: text(sourceRel), TargetRel: text(targetRel), SourceDevice: integer(video.Device), SourceInode: integer(video.Inode), QualityProfile: current.Candidate.QualityProfile, TargetEvidence: evidence}); e != nil {
		return zero, e
	}
	if e = q.AutomaticTVOrganizationInsertPlan(ctx, db.AutomaticTVOrganizationInsertPlanParams{PlanID: plan.ID, OperationID: operation, TaskID: current.Task.ID, ObservationJobID: current.Original.ObservationJobID, OrganizationJobID: lease.ID, StepID: step.ID, AuthorityHash: current.Original.AuthorityHash, ProofHash: current.Original.ProofHash, InventoryStamp: current.Original.InventoryStamp, SourceRoot: sourceRoot, SourceRel: sourceRel, SourceDirectory: sourceDir, SourceRootDevice: sourceIdentity.Device, SourceRootInode: sourceIdentity.Inode, SourceDirectoryDevice: directoryIdentity.Device, SourceDirectoryInode: directoryIdentity.Inode, SourceDevice: video.Device, SourceInode: video.Inode, SourceSize: video.Size, SourceMtime: video.Mtime, RootID: root.ID, RootPath: root.Path, RootGeneration: root.Generation, RootDevice: targetIdentity.Device, RootInode: targetIdentity.Inode, BaselineDevice: baseline.RootDevice, BaselineInode: baseline.RootInode, MarkerFilename: marker, MarkerDevice: markerIdentity.Device, MarkerInode: markerIdentity.Inode, MarkerSize: markerIdentity.Size, MarkerMtime: markerIdentity.Mtime, QualityProfile: current.Candidate.QualityProfile, TargetEvidence: evidence, ObservedAt: observed, ClientSnapshot: current.ClientSnapshot}); e != nil {
		return zero, e
	}
	if _, e = q.AttachDownloadTransferPlan(ctx, db.AttachDownloadTransferPlanParams{ID: current.Task.ID, TransferPlanID: pgtype.UUID{Bytes: plan.ID, Valid: true}}); e != nil {
		return zero, e
	}
	if _, e = lockAutomaticTVOrganizationCurrent(ctx, q, operation); e != nil {
		return zero, e
	}
	if e = automaticTVOrganizationProtected(ctx, q, current.Authority); e != nil {
		return zero, e
	}
	if e = guardOrganization(ctx, q, []ports.JobLease{lease}); e != nil {
		return zero, e
	}
	fresh, e := q.IsTransferObservationFresh(ctx, observed)
	if e != nil {
		return zero, e
	}
	if !fresh.Valid || !fresh.Bool {
		return zero, ErrReviewRequired
	}
	plan, e = q.GetTransferPlanByID(ctx, plan.ID)
	if e != nil {
		return zero, e
	}
	if e = tx.Commit(ctx); e != nil {
		return zero, e
	}
	return plan, nil
}

func mustAutomaticTVProfile(raw []byte) quality.Profile {
	var p quality.Profile
	_ = json.Unmarshal(raw, &p)
	return p
}

// Called outside SQL while the existing physical source/target slots are held.
// Reopen both the configured capability and the video's own directory by name.
func (s *Service) guardAutomaticTVNamespace(ctx context.Context, plan db.TransferPlan, step db.TransferStep) error {
	var f transferTargetEvidence
	if json.Unmarshal(step.TargetEvidence, &f) != nil {
		return ErrReviewRequired
	}
	if f.AutomaticTVOperationID == nil {
		return nil
	}
	p, e := s.queries.AutomaticTVOrganizationPlan(ctx, plan.OperationID)
	if e != nil {
		return automaticTVOrganizationReadError(e)
	}
	if p.PlanID != plan.ID || p.StepID != step.ID || *f.AutomaticTVOperationID != plan.OperationID {
		return ErrReviewRequired
	}
	job, e := s.queries.GetJobByID(ctx, p.OrganizationJobID)
	if e != nil {
		return e
	}
	observed, e := s.queries.GetSearchDatabaseTime(ctx)
	if e != nil {
		return e
	}
	for _, item := range []struct {
		path          string
		device, inode int64
	}{{p.SourceRoot, p.SourceRootDevice, p.SourceRootInode}, {p.SourceDirectory, p.SourceDirectoryDevice, p.SourceDirectoryInode}, {p.RootPath, p.RootDevice, p.RootInode}} {
		root, e := s.files.OpenRoot(ctx, item.path)
		if e != nil {
			return e
		}
		identity, e := root.Identity(ctx)
		root.Close()
		if e != nil {
			return e
		}
		if identity.Device != item.device || identity.Inode != item.inode {
			return ErrReviewRequired
		}
	}
	target, e := s.files.OpenRoot(ctx, p.RootPath)
	if e != nil {
		return e
	}
	defer target.Close()
	marker, e := target.Inspect(ctx, p.MarkerFilename)
	if e != nil {
		return e
	}
	if marker != (ports.FileIdentity{Device: p.MarkerDevice, Inode: p.MarkerInode, Size: p.MarkerSize, Mtime: p.MarkerMtime}) {
		return ErrReviewRequired
	}
	return s.queries.AutomaticTVOrganizationInsertObservation(ctx, db.AutomaticTVOrganizationInsertObservationParams{ID: s.idgen.NewUUID(), PlanID: plan.ID, StepID: step.ID, ExecutionOwner: plan.ExecutionOwner.String, ExecutionToken: plan.ExecutionToken, JobID: job.ID, LeaseOwner: job.LeaseOwner.String, LeaseToken: job.LeaseToken, ObservedAt: observed})
}

// All generic facts/events have been written in this same transaction. Finish
// the original plan/task/job here, then verify the captured deadlines after SQL.
func (s *Service) finishAutomaticTVImportedStep(ctx context.Context, q *db.Queries, plan db.TransferPlan, step db.TransferStep, file db.LibraryFile, authority []ports.JobLease) error {
	var f transferTargetEvidence
	if json.Unmarshal(step.TargetEvidence, &f) != nil {
		return ErrReviewRequired
	}
	if f.AutomaticTVOperationID == nil {
		return nil
	}
	if len(authority) != 1 {
		return ErrExecutionRejected
	}
	lease := authority[0]
	p, e := q.AutomaticTVOrganizationPlan(ctx, plan.OperationID)
	if e != nil {
		return e
	}
	if p.PlanID != plan.ID || p.StepID != step.ID || p.OrganizationJobID != lease.ID || file.RootID != p.RootID || file.SizeBytes != p.SourceSize {
		return ErrReviewRequired
	}
	if _, e = q.LockJobForWrite(ctx, lease.ID); e != nil {
		return e
	}
	job, e := q.GetJobByID(ctx, lease.ID)
	if e != nil {
		return e
	}
	if e = guardOrganization(ctx, q, authority); e != nil {
		return e
	}
	currentPlan, e := q.GetTransferPlanByID(ctx, plan.ID)
	if e != nil {
		return e
	}
	observation, e := q.AutomaticTVOrganizationObservation(ctx, db.AutomaticTVOrganizationObservationParams{PlanID: plan.ID, ExecutionOwner: plan.ExecutionOwner.String, ExecutionToken: plan.ExecutionToken, JobID: lease.ID, LeaseOwner: lease.Owner, LeaseToken: lease.Token})
	if e != nil {
		return automaticTVOrganizationReadError(e)
	}
	if _, e = lockAutomaticTVOrganizationCurrent(ctx, q, plan.OperationID); e != nil {
		return e
	}
	if _, e = q.FinishTransferPlan(ctx, db.FinishTransferPlanParams{ID: plan.ID, Owner: plan.ExecutionOwner.String, Token: plan.ExecutionToken}); e != nil {
		return ErrExecutionRejected
	}
	if _, e = q.FinishDownloadOrganization(ctx, db.FinishDownloadOrganizationParams{ID: p.TaskID, TransferPlanID: pgtype.UUID{Bytes: plan.ID, Valid: true}}); e != nil {
		return e
	}
	result, _ := json.Marshal(map[string]any{"task_id": p.TaskID, "plan_id": plan.ID, "state": "completed", "automatic_tv_inventory_stamp": p.InventoryStamp})
	if _, e = q.FinishJob(ctx, db.FinishJobParams{Column1: lease.ID, Column2: lease.Owner, Column3: lease.Token, Column4: result}); e != nil {
		return ErrExecutionRejected
	}
	if e = q.AutomaticTVOrganizationInsertCompletion(ctx, db.AutomaticTVOrganizationInsertCompletionParams{PlanID: plan.ID, StepID: step.ID, FileID: file.ID, JobID: lease.ID, LeaseOwner: lease.Owner, LeaseToken: lease.Token, LeaseUntil: job.LeaseUntil, PlanLeaseUntil: currentPlan.LeaseUntil, ObservationID: observation.ID, Result: result}); e != nil {
		return e
	}
	if _, e = lockAutomaticTVOrganizationCurrent(ctx, q, plan.OperationID); e != nil {
		return e
	}
	valid, e := q.AutomaticTVOrganizationTail(ctx, plan.ID)
	if e != nil {
		return e
	}
	if !valid.Valid || !valid.Bool {
		return ErrExecutionRejected
	}
	return nil
}

func (s *Service) confirmAutomaticTVOrganization(ctx context.Context, lease ports.JobLease, plan db.TransferPlan) (bool, error) {
	handled, e := s.hasAutomaticTVOrganization(ctx, plan.OperationID)
	if e != nil || !handled {
		return handled, e
	}
	proof, e := s.queries.AutomaticTVOrganizationCompletion(ctx, plan.ID)
	if e != nil {
		return true, automaticTVOrganizationReadError(e)
	}
	if proof.JobID != lease.ID || proof.LeaseOwner != lease.Owner || proof.LeaseToken != lease.Token {
		return true, ErrExecutionRejected
	}
	valid, e := s.queries.AutomaticTVOrganizationCompletionValid(ctx, plan.ID)
	if e != nil {
		return true, e
	}
	if !valid {
		return true, ErrReviewRequired
	}
	return true, nil
}
