package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"testing"
)

func TestFrozenUpgradePublishedInputsInvalidatePG(t *testing.T) {
	changes := map[string]string{
		"intent":            "UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1",
		"source_config":     "UPDATE indexer SET config_version=config_version+1 WHERE id=(SELECT indexer_id FROM release_candidate WHERE id=$1)",
		"downloader_config": "UPDATE download_client SET password_encrypted='different-config'::bytea WHERE id=$1",
		"root_generation":   "UPDATE library_root SET generation=generation+1 WHERE id=$1",
		"old_identity":      "UPDATE library_file SET size_bytes=size_bytes+1 WHERE id=$1",
		"fact_revoked":      "UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE id=$1",
	}
	for name, sql := range changes {
		t.Run(name, func(t *testing.T) {
			f := frozenUpgrade(t)
			f.enable(t)
			c := f.command(t)
			id := f.sub
			switch name {
			case "source_config":
				id = f.candidate
			case "downloader_config":
				id = f.client
			case "root_generation":
				id = f.root
			case "old_identity":
				id = f.file
			case "fact_revoked":
				id = f.fact
			}
			f.exec(t, sql, id)
			before := f.oldState(t)
			if _, e := f.store.ReserveUpgrade(context.Background(), f.actor, c, "stale-"+name); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
				t.Fatalf("stale publication accepted: %v", e)
			}
			f.assertNoReservation(t)
			if before != f.oldState(t) {
				t.Fatal("failed reservation altered old file/fact")
			}
		})
	}
}

func TestFrozenUpgradeStrictQualityAndTitlePG(t *testing.T) {
	for _, name := range []string{"equal_score", "cutoff_reached", "rejected_title", "no_confirmation"} {
		t.Run(name, func(t *testing.T) {
			f := frozenUpgrade(t)
			f.enable(t)
			switch name {
			case "equal_score":
				title := "Fight.Club.1999.1080p.Bluray.x264-GRP"
				profile, _ := json.Marshal(media.Recognize(title).Quality)
				f.exec(t, `UPDATE release_candidate SET title=$2,quality_profile=$3::jsonb WHERE id=$1`, f.candidate, title, string(profile))
			case "cutoff_reached", "rejected_title":
				rules := `{"cutoff_score":300,"resolution_order":{"1080p":300,"2160p":400},"source_order":{"Bluray":40}}`
				if name == "rejected_title" {
					rules = `{"cutoff_score":1000,"resolution_order":{"1080p":300,"2160p":400},"source_order":{"Bluray":40},"reject_groups":["BADGROUP"]}`
					title := "Fight.Club.1999.2160p.Bluray.x265-BADGROUP"
					profile, _ := json.Marshal(media.Recognize(title).Quality)
					f.exec(t, `UPDATE release_candidate SET title=$2,quality_profile=$3::jsonb WHERE id=$1`, f.candidate, title, string(profile))
				}
				f.exec(t, `INSERT INTO policy_revision(id,policy_id,revision,rules) VALUES(gen_random_uuid(),'frozen-upgrade',2,$1::jsonb)`, rules)
				f.exec(t, `UPDATE policy SET current_revision=2 WHERE id='frozen-upgrade'`)
				f.exec(t, `UPDATE subscription SET quality_policy_revision=2,intent_version=intent_version+1 WHERE id=$1`, f.sub)
			}
			if name == "cutoff_reached" || name == "rejected_title" {
				frozenProjectRevision(t, f, 2)
			} else {
				f.project(t)
			}
			v, e := postgres.NewUpgradePreviewStore(f.p).ReadUpgradePreview(context.Background(), f.actor, ports.UpgradePreviewRequest{SubscriptionID: f.sub, CurrentFileID: f.file, CandidateID: f.candidate})
			if e != nil {
				t.Fatal(e)
			}
			if v.HoldReason != "" {
				t.Fatal("quality fixture is not authoritative", v.HoldReason)
			}
			// Use the *current* observation, so rejection must re-evaluate raw evidence;
			// simply rejecting an old preview version would not prove the quality rule.
			c := ports.ReserveUpgradeCommand{SubscriptionID: f.sub, CurrentFileID: f.file, CandidateID: f.candidate, Expected: v.Version, ExpectedClientDigest: frozenClientDigest(t, f), Confirmed: name != "no_confirmation"}
			if _, e = f.store.ReserveUpgrade(context.Background(), f.actor, c, "quality-"+name); !errors.Is(e, ports.ErrUpgradeReservationConflict) && !errors.Is(e, ports.ErrBadResponse) {
				t.Fatalf("fresh bad quality/confirmation accepted: %v", e)
			}
			f.assertNoReservation(t)
		})
	}
}

func TestFrozenUpgradeFrozenIdentityEvidencePG(t *testing.T) {
	f := frozenUpgrade(t)
	f.enable(t)
	c := f.command(t)
	r, e := f.store.ReserveUpgrade(context.Background(), f.actor, c, "snapshot")
	if e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e = f.p.QueryRow(context.Background(), "SELECT snapshot FROM movie_upgrade_authority WHERE operation_id=$1", r.OperationID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var snap struct {
		Identities map[string]json.RawMessage `json:"frozen_identities"`
	}
	if e = json.Unmarshal(raw, &snap); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"file", "fact", "unit", "root", "subscription", "candidate", "source", "source_observations"} {
		if len(snap.Identities[key]) == 0 || string(snap.Identities[key]) == "null" {
			t.Fatal("missing frozen identity", key)
		}
	}
	var file map[string]json.RawMessage
	if e = json.Unmarshal(snap.Identities["file"], &file); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"file_fingerprint", "size_bytes", "mtime_ns"} {
		if len(file[key]) == 0 {
			t.Fatal("missing observed file", key)
		}
	}
	var source, candidate map[string]json.RawMessage
	_ = json.Unmarshal(snap.Identities["source"], &source)
	_ = json.Unmarshal(snap.Identities["candidate"], &candidate)
	for _, key := range []string{"api_key_encrypted", "url", "password_encrypted", "link_encrypted", "guid"} {
		if source[key] != nil || candidate[key] != nil {
			t.Fatal("sensitive source material persisted", key)
		}
	}
}
