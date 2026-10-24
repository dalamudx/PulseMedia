//go:build linux

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

func TestM8UntrackedOrganizationHardlinkRuntimeCookiePG(t *testing.T) {
	m8UntrackedOrganizationRequired(t)
	f := m8UntrackedOrganizationNew(t, "hardlink")
	history := f.confirmationHistory(t)
	receipt, raw := f.queue(t)
	f.deniedIdentities(t, receipt)
	f.start(t)
	f.completed(t, receipt)
	f.stop(t)
	f.verifyImport(t, receipt, "hardlink")
	f.recoverOffline(t, receipt, raw, "completed")
	if history != f.confirmationHistory(t) {
		t.Fatal("schema105 execution changed original schema104 history")
	}
	f.log(t, "movie_hardlink", 1)
}

func TestM8UntrackedOrganizationCopyMoveRuntimeCookiePG(t *testing.T) {
	m8UntrackedOrganizationRequired(t)
	for _, action := range []string{"copy", "move"} {
		t.Run(action, func(t *testing.T) {
			f := m8UntrackedOrganizationNew(t, action)
			history := f.confirmationHistory(t)
			receipt, raw := f.queue(t)
			f.start(t)
			f.completed(t, receipt)
			f.stop(t)
			f.verifyImport(t, receipt, action)
			f.recoverOffline(t, receipt, raw, "completed")
			if history != f.confirmationHistory(t) {
				t.Fatal("copy/move changed original schema104 history")
			}
			f.log(t, "movie_"+action, 1)
		})
	}
}

func TestM8UntrackedOrganizationFinalizeRecoveryRuntimeCookiePG(t *testing.T) {
	m8UntrackedOrganizationFinalizeRecovery(t, "hardlink")
}

func TestM8UntrackedOrganizationCopyMoveFinalizeRecoveryRuntimeCookiePG(t *testing.T) {
	for _, action := range []string{"copy", "move"} {
		t.Run(action, func(t *testing.T) { m8UntrackedOrganizationFinalizeRecovery(t, action) })
	}
}

func m8UntrackedOrganizationFinalizeRecovery(t *testing.T, action string) {
	t.Helper()
	m8UntrackedOrganizationRequired(t)
	f := m8UntrackedOrganizationNew(t, action)
	m8UntrackedOrganizationFinalizeRecoveryFixture(t, f)
}

func m8UntrackedOrganizationFinalizeRecoveryFixture(t *testing.T, f *m8UntrackedOrganizationFixture) {
	t.Helper()
	action := f.confirmation.Material.Input.Action
	history := f.confirmationHistory(t)
	receipt, raw := f.queue(t)
	release := f.failFirstFinalization(t, receipt)
	f.start(t)
	f.wait(t, "first tail rollback after native target publication", func() bool {
		if !f.predicate(t, `SELECT is_called FROM m8_untracked_organization_tail_sequence`) {
			return false
		}
		_, err := os.Stat(filepath.Join(f.target, f.confirmation.Material.Input.TargetRelativePath))
		return err == nil && f.predicate(t, `SELECT (state='queued' AND attempts=1) OR (state='running' AND attempts=2) FROM job WHERE id=$1`, receipt.Operation.JobID)
	})
	// A session-owned advisory gate holds the next successful finalization. These
	// reads cannot race a recovery commit, even if the five-second retry elapsed.
	f.count(t, `SELECT count(*) FROM untracked_organization_completion`, 0)
	f.count(t, `SELECT count(*) FROM untracked_organization_observation`, 0)
	f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, 0)
	f.count(t, `SELECT count(*) FROM library_file_unit`, 0)
	f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='media.imported'`, 0)
	f.count(t, `SELECT count(*) FROM audit_log WHERE action='untracked.organization.imported'`, 0)
	f.count(t, `SELECT count(*) FROM job WHERE kind IN ('media_sidecar','media_subtitle')`, 0)
	f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state<>'succeeded' AND attempts BETWEEN 1 AND 2 AND max_attempts=3`, 1, receipt.Operation.JobID)
	f.count(t, `SELECT count(*) FROM untracked_organization_permit WHERE operation_id=$1 AND job_token=1 AND execution_token=1`, 1, receipt.Operation.ID)
	targetBefore := m8UntrackedOrganizationIdentity(t, f.target, f.confirmation.Material.Input.TargetRelativePath)
	if action == "hardlink" && targetBefore != f.sourceIdentity {
		t.Fatal("first physical target was not the frozen source hardlink")
	}
	if action != "hardlink" && targetBefore.Device == f.sourceIdentity.Device && targetBefore.Inode == f.sourceIdentity.Inode {
		t.Fatal("copy/move target aliases original source")
	}
	body, err := os.ReadFile(filepath.Join(f.target, f.confirmation.Material.Input.TargetRelativePath))
	if err != nil || !bytes.Equal(body, f.sourceBytes) || sha256.Sum256(body) != sha256.Sum256(f.sourceBytes) {
		t.Fatal("published target lost complete bytes before finalization rollback")
	}
	if action == "move" {
		if _, err := os.Lstat(filepath.Join(f.source, f.sourceRelative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("move did not remove the original source before database rollback")
		}
	} else if m8UntrackedOrganizationIdentity(t, f.source, f.sourceRelative) != f.sourceIdentity {
		t.Fatal("rollback changed original source")
	}
	permitBefore := m8ConfigSnapshot(t, f.m7ScrapeFixture, "untracked_organization_permit", "transfer_content_evidence", "untracked_organization_operation", "untracked_organization_unit")
	frozenBefore := f.frozenTransfer(t, receipt)
	release()
	f.completed(t, receipt)
	f.stop(t)
	if permitBefore != m8ConfigSnapshot(t, f.m7ScrapeFixture, "untracked_organization_permit", "transfer_content_evidence", "untracked_organization_operation", "untracked_organization_unit") || frozenBefore != f.frozenTransfer(t, receipt) {
		t.Fatal("recovery rewrote original permit, execution owner/token or frozen transfer")
	}
	if targetBefore != m8UntrackedOrganizationIdentity(t, f.target, f.confirmation.Material.Input.TargetRelativePath) {
		t.Fatal("recovery replaced the already published target inode")
	}
	f.count(t, `SELECT count(*) FROM untracked_organization_observation observation JOIN untracked_organization_permit permit USING(operation_id,job_id) JOIN job j ON j.id=observation.job_id WHERE observation.operation_id=$1 AND permit.job_token=1 AND observation.job_token=2 AND observation.job_owner=j.lease_owner AND observation.job_token=j.lease_token AND j.attempts=2 AND j.state='succeeded'`, 1, receipt.Operation.ID)
	f.count(t, `SELECT last_value FROM m8_untracked_organization_tail_sequence`, 2)
	f.verifyImport(t, receipt, action)
	f.recoverOffline(t, receipt, raw, "completed")
	if history != f.confirmationHistory(t) {
		t.Fatal("recovery changed original schema104 confirmation")
	}
	// The production filesystem is fixed in App assembly. Stable inode and one
	// immutable permit prove preserved effects; they do not count native calls.
	f.log(t, "finalization_tail_rollback_observe_token2", 1)
}

func TestM8UntrackedOrganizationQueuedGuardsRuntimeCookiePG(t *testing.T) {
	m8UntrackedOrganizationRequired(t)
	for _, scenario := range []string{"source_changed", "scope_disabled", "target_exists"} {
		t.Run(scenario, func(t *testing.T) {
			f := m8UntrackedOrganizationNew(t, "copy")
			history := f.confirmationHistory(t)
			receipt, raw := f.queue(t)
			var targetBefore string
			switch scenario {
			case "source_changed":
				changed := append(append([]byte(nil), f.sourceBytes...), []byte(" externally changed")...)
				if err := os.WriteFile(filepath.Join(f.source, "Movie.2026.mkv"), changed, 0600); err != nil {
					t.Fatal("change owned source before physical permit")
				}
			case "scope_disabled":
				f.expect(t, http.MethodPut, f.path(), f.body(t, f.confirmation.Material.Scope.Version, false), "organization-disable", http.StatusOK)
			case "target_exists":
				destination := filepath.Join(f.target, f.confirmation.Material.Input.TargetRelativePath)
				if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
					t.Fatal("create owned destination parent")
				}
				if err := os.WriteFile(destination, []byte("user destination must never be overwritten"), 0600); err != nil {
					t.Fatal("create owned conflicting destination")
				}
				targetBefore = m8UntrackedOrganizationTree(t, f.target)
			}
			sourceBefore := m8UntrackedOrganizationTree(t, f.source)
			f.start(t)
			f.wait(t, "bounded queued work rejected without a physical permit", func() bool {
				return f.predicate(t, `SELECT state='dead' AND attempts=3 AND max_attempts=3 FROM job WHERE id=$1`, receipt.Operation.JobID)
			})
			f.stop(t)
			f.count(t, `SELECT count(*) FROM untracked_organization_permit`, 0)
			f.count(t, `SELECT count(*) FROM transfer_content_evidence`, 0)
			f.count(t, `SELECT count(*) FROM untracked_organization_completion`, 0)
			f.count(t, `SELECT count(*) FROM untracked_organization_observation`, 0)
			f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, 0)
			f.count(t, `SELECT count(*) FROM library_file_unit`, 0)
			f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='media.imported'`, 0)
			if scenario == "target_exists" {
				if targetBefore != m8UntrackedOrganizationTree(t, f.target) {
					t.Fatal("queued copy overwrote or changed the existing destination")
				}
			} else if _, err := os.Lstat(filepath.Join(f.target, f.confirmation.Material.Input.TargetRelativePath)); !os.IsNotExist(err) {
				t.Fatal("stale or disabled queued work published a target")
			}
			if sourceBefore != m8UntrackedOrganizationTree(t, f.source) || f.protected != f.protectedGraph(t) || f.calls.Load() != 0 {
				t.Fatal("guard rejection touched source, acquisition graph or downloader")
			}
			f.recoverOffline(t, receipt, raw, "failed")
			if history != f.confirmationHistory(t) {
				t.Fatal("queued guard rejection changed schema104 history")
			}
			f.log(t, "queued_"+scenario, 0)
		})
	}
}

func (f *m8UntrackedOrganizationFixture) queue(t *testing.T) (ports.UntrackedOrganizationReceipt, []byte) {
	t.Helper()
	body := m8UntrackedOrganizationJSON(t, ports.UntrackedOrganizationInput{ConfirmationID: f.confirmation.Material.ID})
	raw := f.expect(t, http.MethodPost, f.base+"/organizations", body, "organization-original", http.StatusAccepted)
	var receipt ports.UntrackedOrganizationReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.ExecutionAllowed || receipt.State != "queued" || receipt.CommandKey != "organization-original" || receipt.Operation.ActorID != f.admin || receipt.Operation.ConfirmationID != f.confirmation.Material.ID || receipt.Operation.ScopeID != f.scope || receipt.Operation.FileID != f.fileID || receipt.Operation.ID == uuid.Nil || receipt.Operation.PlanID == uuid.Nil || receipt.Operation.StepID == uuid.Nil || receipt.Operation.JobID == uuid.Nil {
		t.Fatal("organization command lost immutable queued identity")
	}
	f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 1)
	f.count(t, `SELECT count(*) FROM transfer_plan WHERE id=$1 AND operation_id=$2 AND state='approved' AND requires_approval AND strategy=$3 AND execution_owner IS NULL AND execution_token=0`, 1, receipt.Operation.PlanID, receipt.Operation.ID, f.confirmation.Material.Input.Action)
	f.count(t, `SELECT count(*) FROM transfer_step WHERE id=$1 AND plan_id=$2 AND state='pending' AND target_file_id IS NULL AND source_device=$3 AND source_inode=$4 AND source_size=$5 AND source_mtime=$6`, 1, receipt.Operation.StepID, receipt.Operation.PlanID, f.sourceIdentity.Device, f.sourceIdentity.Inode, f.sourceIdentity.Size, f.sourceIdentity.Mtime)
	f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND kind='untracked_organization' AND state='queued' AND attempts=0 AND lease_token=0 AND max_attempts=3 AND payload=jsonb_build_object('operation_id',$2::uuid)`, 1, receipt.Operation.JobID, receipt.Operation.ID)
	f.count(t, `SELECT count(*) FROM untracked_organization_permit`, 0)
	f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, 0)
	return receipt, raw
}

func (f *m8UntrackedOrganizationFixture) completed(t *testing.T, receipt ports.UntrackedOrganizationReceipt) {
	t.Helper()
	f.wait(t, "atomic organization completion through production App executor", func() bool {
		return f.predicate(t, `SELECT EXISTS(SELECT 1 FROM untracked_organization_completion c JOIN untracked_organization_operation o ON o.id=c.operation_id JOIN job j ON j.id=o.job_id JOIN transfer_plan p ON p.id=o.plan_id JOIN transfer_step s ON s.id=o.step_id WHERE o.id=$1 AND j.state='succeeded' AND p.state='completed' AND s.state='completed' AND s.target_file_id=c.file_id)`, receipt.Operation.ID)
	})
	body := f.expect(t, http.MethodGet, f.base+"/organizations/"+receipt.Operation.ID.String(), "", "", http.StatusOK)
	var view ports.UntrackedOrganizationView
	if json.Unmarshal(body, &view) != nil || view.State != "completed" || view.ExecutionAllowed || view.ReviewRequired || view.CompletedFileID == nil || view.RecordedAt == nil || view.Operation.ID != receipt.Operation.ID {
		t.Fatal("live App GET did not expose genuine persisted completion")
	}
}

func (f *m8UntrackedOrganizationFixture) verifyImport(t *testing.T, receipt ports.UntrackedOrganizationReceipt, action string) {
	t.Helper()
	units := len(f.confirmation.Material.Review.Evidence.Candidate.Units)
	targetIdentity := m8UntrackedOrganizationIdentity(t, f.target, f.confirmation.Material.Input.TargetRelativePath)
	target, err := os.ReadFile(filepath.Join(f.target, f.confirmation.Material.Input.TargetRelativePath))
	if err != nil || !bytes.Equal(target, f.sourceBytes) || sha256.Sum256(target) != sha256.Sum256(f.sourceBytes) {
		t.Fatal("native target lost full bytes or digest")
	}
	if action == "hardlink" {
		if targetIdentity != f.sourceIdentity {
			t.Fatal("native hardlink did not preserve frozen source identity")
		}
	} else if targetIdentity.Device == f.sourceIdentity.Device && targetIdentity.Inode == f.sourceIdentity.Inode {
		t.Fatal("native copy/move reused the source inode")
	}
	if action == "move" {
		if _, err = os.Lstat(filepath.Join(f.source, f.sourceRelative)); !os.IsNotExist(err) {
			t.Fatal("verified move retained original source")
		}
	} else {
		source, e := os.ReadFile(filepath.Join(f.source, f.sourceRelative))
		if e != nil || !bytes.Equal(source, f.sourceBytes) || m8UntrackedOrganizationIdentity(t, f.source, f.sourceRelative) != f.sourceIdentity {
			t.Fatal("organization modified original source bytes or identity")
		}
	}
	for name, want := range map[string]string{"user.nfo": "user sidecar remains", ".scope-marker": "owned"} {
		body, e := os.ReadFile(filepath.Join(f.target, name))
		if e != nil || string(body) != want {
			t.Fatal("organization overwrote user sidecar or root marker")
		}
	}
	digest := sha256.Sum256(f.sourceBytes)
	f.count(t, `SELECT count(*) FROM untracked_organization_permit WHERE operation_id=$1 AND content_size=$2 AND encode(content_sha256,'hex')=$3`, 1, receipt.Operation.ID, len(f.sourceBytes), hex.EncodeToString(digest[:]))
	if f.confirmation.Material.Review.Evidence.Candidate.Namespace == "tmdb.movie" {
		f.count(t, `SELECT count(*) FROM untracked_organization_unit ou JOIN media_unit u ON u.id=ou.unit_id JOIN media_identity i ON i.media_id=u.media_id WHERE ou.operation_id=$1 AND u.unit_key='movie' AND NOT u.retired AND i.namespace='tmdb.movie' AND i.external_id='551' AND ou.tmdb_episode_id=0`, 1, receipt.Operation.ID)
	} else {
		f.count(t, `SELECT count(*) FROM untracked_organization_unit ou JOIN media_unit u ON u.id=ou.unit_id JOIN media_identity i ON i.media_id=u.media_id WHERE ou.operation_id=$1 AND NOT u.retired AND i.namespace='tmdb.tv' AND i.external_id='551' AND ((u.unit_key='S01E02' AND ou.tmdb_episode_id=55102 AND ou.candidate_unit->>'episode'='2' AND ou.candidate_unit->>'air_date' IS NOT NULL) OR (u.unit_key='S01E07' AND ou.tmdb_episode_id=55107 AND ou.candidate_unit->>'episode'='7' AND ou.candidate_unit->'air_date'='null'::jsonb))`, 2, receipt.Operation.ID)
	}
	f.count(t, `SELECT count(*) FROM media`, 1)
	f.count(t, `SELECT count(*) FROM media_unit`, units)
	f.count(t, `SELECT count(*) FROM untracked_organization_unit`, units)
	f.count(t, `SELECT count(*) FROM library_file WHERE root_id=$1 AND retired_at IS NULL`, f.baselineFiles+1, f.root)
	f.count(t, `SELECT count(*) FROM library_file WHERE root_id=$1 AND rel_path=$2 AND retired_at IS NULL`, 1, f.root, f.confirmation.Material.Input.TargetRelativePath)
	f.count(t, `SELECT count(*) FROM untracked_organization_completion c JOIN library_file file ON file.id=c.file_id JOIN library_file_unit mapping ON mapping.file_id=file.id JOIN untracked_organization_unit ou ON ou.operation_id=c.operation_id AND ou.unit_id=mapping.unit_id JOIN unit_fact fact ON fact.source_file_id=file.id AND fact.unit_id=ou.unit_id JOIN transfer_step step ON step.id=c.step_id WHERE c.operation_id=$1 AND file.root_id=$2 AND file.rel_path=$3 AND file.size_bytes=$4 AND file.mtime_ns=$5 AND mapping.retired_at IS NULL AND fact.root_id=file.root_id AND fact.stage='imported' AND fact.revoked_at IS NULL AND fact.source_task_id IS NULL AND fact.quality_profile=step.quality_profile AND (fact.expires_at IS NULL OR fact.expires_at>clock_timestamp())`, units, receipt.Operation.ID, f.root, f.confirmation.Material.Input.TargetRelativePath, targetIdentity.Size, targetIdentity.Mtime)
	f.count(t, `SELECT count(*) FROM library_file_unit WHERE retired_at IS NULL`, units)
	f.count(t, `SELECT count(*) FROM unit_fact`, units)
	f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported' AND revoked_at IS NULL`, units)
	f.count(t, `SELECT count(*) FROM outbox_event event JOIN untracked_organization_completion c ON c.operation_id=$1 JOIN untracked_organization_unit ou ON ou.operation_id=c.operation_id WHERE event.event_type='media.imported' AND event.aggregate_type='media' AND event.aggregate_id=ou.unit_id::text AND event.payload->>'operation_id'=c.operation_id::text AND event.payload->>'file_id'=c.file_id::text AND event.payload->>'unit_id'=ou.unit_id::text AND event.payload->>'target_path'=$2`, units, receipt.Operation.ID, filepath.Join(f.target, f.confirmation.Material.Input.TargetRelativePath))
	f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='media.imported'`, units)
	f.count(t, `SELECT count(*) FROM audit_log WHERE action='untracked.organization.imported' AND target_id=$1`, 1, receipt.Operation.ID.String())
	f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 1)
	f.count(t, `SELECT count(*) FROM untracked_organization_permit`, 1)
	f.count(t, `SELECT count(*) FROM untracked_organization_observation`, 1)
	f.count(t, `SELECT count(*) FROM untracked_organization_completion`, 1)
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_organization'`, 1)
	f.count(t, `SELECT count(*) FROM transfer_plan WHERE operation_id=$1`, 1, receipt.Operation.ID)
	f.count(t, `SELECT count(*) FROM transfer_step WHERE plan_id=$1`, 1, receipt.Operation.PlanID)
	if f.protected != f.protectedGraph(t) || f.calls.Load() != 0 {
		t.Fatal("native import changed acquisition graph or called downloader")
	}
}

func (f *m8UntrackedOrganizationFixture) confirmationHistory(t *testing.T) string {
	t.Helper()
	return m8ConfigSnapshot(t, f.m7ScrapeFixture, "untracked_review_request", "untracked_review_request_receipt", "untracked_review_result", "untracked_review_confirmation")
}

func (f *m8UntrackedOrganizationFixture) recoverOffline(t *testing.T, receipt ports.UntrackedOrganizationReceipt, raw []byte, state string) {
	t.Helper()
	if f.cancel != nil {
		t.Fatal("read-only verification requires all owned workers joined")
	}
	before := f.allTables(t)
	metadataCalls, downloaderCalls := f.metadataCalls.Load(), f.calls.Load()
	sourceTree := m8UntrackedOrganizationTree(t, f.source)
	targetTree := m8UntrackedOrganizationTree(t, f.target)
	sourceOffline, targetOffline := f.source+".offline", f.target+".offline"
	if err := os.Rename(f.source, sourceOffline); err != nil {
		t.Fatal("take owned source offline")
	}
	defer func() {
		if err := os.Rename(sourceOffline, f.source); err != nil {
			t.Error("restore owned source namespace")
		}
	}()
	if err := os.Rename(f.target, targetOffline); err != nil {
		t.Fatal("take owned target offline")
	}
	defer func() {
		if err := os.Rename(targetOffline, f.target); err != nil {
			t.Error("restore owned target namespace")
		}
	}()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path, body, key, status := f.base+"/organization-commands/organization-original", "", "", http.StatusOK
		if method == http.MethodPost {
			path, body, key, status = f.base+"/organizations", m8UntrackedOrganizationJSON(t, ports.UntrackedOrganizationInput{ConfirmationID: f.confirmation.Material.ID}), "organization-original", http.StatusAccepted
		}
		got := f.expect(t, method, path, body, key, status)
		if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(raw)) {
			t.Fatal("offline original organization key changed immutable queued receipt")
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path, body, key := f.base+"/confirmation-commands/organization-confirmation", "", ""
		if method == http.MethodPost {
			path, body, key = f.base+"/confirmations", f.confirmationBody, "organization-confirmation"
		}
		got := f.expect(t, method, path, body, key, http.StatusOK)
		if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(f.confirmationRaw)) {
			t.Fatal("organization changed original false schema104 confirmation receipt")
		}
	}
	got := f.expect(t, http.MethodGet, f.base+"/review-commands/organization-review", "", "", http.StatusOK)
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(f.reviewRaw)) {
		t.Fatal("offline review key changed original request receipt")
	}
	got = f.expect(t, http.MethodGet, f.base+"/organizations/"+receipt.Operation.ID.String(), "", "", http.StatusOK)
	var view ports.UntrackedOrganizationView
	if json.Unmarshal(got, &view) != nil || view.State != state || view.ExecutionAllowed || view.Operation.ID != receipt.Operation.ID {
		t.Fatal("offline persisted GET lost original operation or terminal state")
	}
	if before != f.allTables(t) || metadataCalls != f.metadataCalls.Load() || downloaderCalls != f.calls.Load() || sourceTree != m8UntrackedOrganizationTree(t, sourceOffline) || targetTree != m8UntrackedOrganizationTree(t, targetOffline) {
		t.Fatal("GET/original-key replay changed full business graph, native bytes/identities or network counts")
	}
}

func (f *m8UntrackedOrganizationFixture) deniedIdentities(t *testing.T, receipt ports.UntrackedOrganizationReceipt) {
	t.Helper()
	actor, err := f.auth.AuthenticateToken(t.Context(), f.cookie.Value)
	if err != nil {
		t.Fatal("authenticate genuine owner Cookie")
	}
	tokens := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.pool, f.app.IDGen()), crypto.ReadTokenIssuer{}, f.app.Clock())
	_, bearer, err := tokens.Create(t.Context(), actor, ports.TokenCommand{Name: "Owned organization read token"}, "organization-read-token")
	if err != nil {
		t.Fatal("create real read Token")
	}
	memberID := uuid.New()
	f.exec(t, `INSERT INTO app_user SELECT (jsonb_populate_record(NULL::app_user,to_jsonb(u)||jsonb_build_object('id',$1::text,'username','organization-member','role','member'))).* FROM app_user u WHERE id=$2`, memberID, f.admin)
	member := f.login(t, "organization-member")
	before := f.allTables(t)
	source, target := m8UntrackedOrganizationTree(t, f.source), m8UntrackedOrganizationTree(t, f.target)
	metadataCalls := f.metadataCalls.Load()
	for _, identity := range []struct {
		cookie *http.Cookie
		bearer string
	}{{member, ""}, {nil, bearer}} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			path, body, key := f.base+"/organization-commands/organization-original", "", ""
			if method == http.MethodPost {
				path, body, key = f.base+"/organizations", m8UntrackedOrganizationJSON(t, ports.UntrackedOrganizationInput{ConfirmationID: f.confirmation.Material.ID}), "organization-forbidden"
			}
			code, _, e := f.call(t, method, path, body, key, identity.cookie, identity.bearer)
			if e != nil || code != http.StatusForbidden {
				t.Fatal("member/Token could read or execute owner organization")
			}
		}
		code, _, e := f.call(t, http.MethodGet, f.base+"/organizations/"+receipt.Operation.ID.String(), "", "", identity.cookie, identity.bearer)
		if e != nil || code != http.StatusForbidden {
			t.Fatal("member/Token could read owner organization status")
		}
	}
	if before != f.allTables(t) || source != m8UntrackedOrganizationTree(t, f.source) || target != m8UntrackedOrganizationTree(t, f.target) || metadataCalls != f.metadataCalls.Load() || f.calls.Load() != 0 {
		t.Fatal("denied identity changed protected rows, filesystem or native call count")
	}
}

func (f *m8UntrackedOrganizationFixture) frozenTransfer(t *testing.T, receipt ports.UntrackedOrganizationReceipt) string {
	t.Helper()
	var frozen string
	if err := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_array(to_jsonb(p)-ARRAY['state','updated_at','finished_at','lease_until'],to_jsonb(s)-ARRAY['state','updated_at','finished_at','target_file_id','error'])::text FROM transfer_plan p JOIN transfer_step s ON s.plan_id=p.id WHERE p.id=$1 AND s.id=$2`, receipt.Operation.PlanID, receipt.Operation.StepID).Scan(&frozen); err != nil {
		t.Fatal("read exact frozen execution and source/target evidence")
	}
	return frozen
}

func (f *m8UntrackedOrganizationFixture) failFirstFinalization(t *testing.T, receipt ports.UntrackedOrganizationReceipt) func() {
	t.Helper()
	gate, err := f.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal("acquire owned finalization gate connection")
	}
	const gateKey int64 = 805105
	if _, err = gate.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, gateKey); err != nil {
		gate.Release()
		t.Fatal("hold owned finalization gate")
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
			defer cancel()
			var unlocked bool
			if e := gate.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, gateKey).Scan(&unlocked); e != nil || !unlocked {
				_ = gate.Conn().Close(ctx)
				t.Error("release owned finalization gate failed")
			}
			gate.Release()
		})
	}
	// Registered after App cleanup: unblock first, then cancel/join production I/O.
	t.Cleanup(release)
	f.exec(t, `CREATE SEQUENCE m8_untracked_organization_tail_sequence`)
	f.exec(t, `CREATE FUNCTION m8_untracked_organization_fail_tail() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE attempt bigint;
BEGIN
 IF NEW.id<>TG_ARGV[0]::uuid OR NEW.kind<>'untracked_organization' OR NEW.state<>'succeeded' THEN RETURN NULL; END IF;
 IF NOT EXISTS(
  SELECT 1 FROM untracked_organization_operation o
  JOIN untracked_organization_completion c ON c.operation_id=o.id
  JOIN untracked_organization_observation observation ON observation.id=c.observation_id
  JOIN transfer_plan p ON p.id=o.plan_id AND p.state='completed'
  JOIN transfer_step s ON s.id=o.step_id AND s.state='completed' AND s.target_file_id=c.file_id
  JOIN untracked_organization_unit u ON u.operation_id=o.id
  JOIN library_file_unit fu ON fu.file_id=c.file_id AND fu.unit_id=u.unit_id AND fu.retired_at IS NULL
  JOIN unit_fact fact ON fact.source_file_id=c.file_id AND fact.unit_id=u.unit_id AND fact.stage='imported' AND fact.revoked_at IS NULL
  JOIN outbox_event event ON event.event_type='media.imported' AND event.payload->>'operation_id'=o.id::text AND event.payload->>'file_id'=c.file_id::text
  WHERE o.job_id=NEW.id
 ) THEN RAISE EXCEPTION 'owned fixture did not reach complete atomic tail'; END IF;
 attempt:=nextval('m8_untracked_organization_tail_sequence');
 IF attempt=1 THEN RAISE EXCEPTION 'owned one-time organization finalization tail failure'; END IF;
 PERFORM pg_advisory_xact_lock(805105::bigint);
 RETURN NULL;
END $$`)
	f.exec(t, fmt.Sprintf(`CREATE CONSTRAINT TRIGGER m8_untracked_organization_tail_failure AFTER UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.kind='untracked_organization' AND NEW.state='succeeded') EXECUTE FUNCTION m8_untracked_organization_fail_tail('%s')`, receipt.Operation.JobID.String()))
	// All objects are in this fixture's disposable PG database; its owner drops
	// that database after workers, gate and assertion pool have been drained.
	return release
}

func (f *m8UntrackedOrganizationFixture) log(t *testing.T, scenario string, imported int) {
	t.Helper()
	if t.Failed() {
		return
	}
	t.Logf("M8_UNTRACKED_ORGANIZATION_APP_PASS scenario=%s app=REAL cookie=REAL PG=18.6 rootfs=REAL metadata=NATIVE_OWNED_HTTP downloader_calls=0 imported=%d", scenario, imported)
}
