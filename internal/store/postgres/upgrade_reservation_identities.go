package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/jackc/pgx/v5"
)

// Freeze observed identities as well as their protected digest, so historical
// review does not depend on reading a later, changed file/candidate row. Source
// credentials, URLs, GUIDs and encrypted references are deliberately omitted;
// their complete original configuration remains bound by ProtectedDigest.
func upgradeFrozenIdentities(ctx context.Context, tx pgx.Tx, v *ports.UpgradePreviewSnapshot) (json.RawMessage, error) {
	var raw json.RawMessage
	e := tx.QueryRow(ctx, `SELECT jsonb_build_object(
 'file',(SELECT to_jsonb(x) FROM library_file x WHERE id=$1),
 'file_units',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM library_file_unit x WHERE file_id=$1),
 'fact',(SELECT to_jsonb(x) FROM unit_fact x WHERE id=$2),
 'unit',(SELECT to_jsonb(x) FROM media_unit x WHERE id=$3),
 'media',(SELECT to_jsonb(x) FROM media x WHERE id=$4),
 'identities',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM media_identity x WHERE media_id=$4),
 'root',(SELECT to_jsonb(x) FROM library_root x WHERE id=$5),
 'subscription',(SELECT to_jsonb(x) FROM subscription x WHERE id=$6),
 'candidate',(SELECT to_jsonb(x)-ARRAY['guid','link_encrypted'] FROM release_candidate x WHERE id=$7),
 'source',(SELECT jsonb_build_object('id',id,'config_version',config_version,'health',health) FROM indexer WHERE id=$8),
 'source_observations',(SELECT jsonb_agg(jsonb_build_object('id',x.id,'indexer_id',x.indexer_id,'config_version',x.config_version,'state',x.state,'finished_at',x.finished_at) ORDER BY x.id) FROM indexer_query x WHERE id IN (SELECT query_id FROM indexer_query_candidate WHERE candidate_id=$7)),
 'wanted',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM wanted_item x WHERE subscription_id=$6))`, v.CurrentFileID, v.CurrentFactID, v.Version.UnitID, v.Version.MediaID, v.Version.RootID, v.Version.SubscriptionID, v.CandidateID, v.SourceID).Scan(&raw)
	return raw, e
}
