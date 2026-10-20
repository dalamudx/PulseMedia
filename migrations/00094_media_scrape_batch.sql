-- +goose Up
CREATE TABLE media_scrape_batch (
 id UUID PRIMARY KEY,
 command_key TEXT NOT NULL CHECK (length(command_key) BETWEEN 1 AND 128),
 requested_by UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(requested_by,command_key)
);
CREATE TABLE media_scrape_item (
 batch_id UUID NOT NULL REFERENCES media_scrape_batch(id) ON DELETE RESTRICT,
 job_id UUID NOT NULL UNIQUE REFERENCES job(id) ON DELETE RESTRICT,
 file_id UUID NOT NULL REFERENCES library_file(id) ON DELETE RESTRICT,
 unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE RESTRICT,
 source_event_id UUID NOT NULL REFERENCES outbox_event(id) ON DELETE RESTRICT,
 PRIMARY KEY(batch_id,file_id,unit_id)
);
CREATE INDEX media_scrape_batch_created_idx ON media_scrape_batch(created_at DESC,id DESC);
CREATE INDEX media_sidecar_subject_idx ON job((payload->>'file_id'),(payload->>'unit_id'),created_at DESC,id DESC) WHERE kind='media_sidecar';

-- Historical task outcomes are observations, never proof that a file still exists.
CREATE VIEW media_scrape_candidate AS
WITH imported AS (
 SELECT DISTINCT ON (f.id,u.id) f.id AS file_id,u.id AS unit_id,e.id AS source_event_id,
 m.id AS media_id,m.media_type,m.title,f.rel_path
 FROM library_file f
 JOIN library_root r ON r.id=f.root_id
 JOIN library_file_unit fu ON fu.file_id=f.id AND fu.retired_at IS NULL
 JOIN media_unit u ON u.id=fu.unit_id AND NOT u.retired
 JOIN media m ON m.id=u.media_id
 JOIN outbox_event e ON e.aggregate_type='media' AND e.aggregate_id=u.id::text
  AND e.event_type='media.imported' AND e.payload->>'file_id'=f.id::text AND e.payload->>'unit_id'=u.id::text
 WHERE f.retired_at IS NULL AND r.health='online' AND r.baseline_completed
  AND r.media_type=m.media_type AND m.media_type IN ('movie','tv')
  AND EXISTS(SELECT 1 FROM unit_fact uf WHERE uf.source_file_id=f.id AND uf.unit_id=u.id AND uf.root_id=r.id AND uf.stage='imported' AND uf.revoked_at IS NULL)
 ORDER BY f.id,u.id,e.created_at DESC,e.id DESC
)
SELECT i.*,COALESCE(active.state,latest.state,'missing')::text AS status
FROM imported i
LEFT JOIN LATERAL (
 SELECT j.state FROM job j WHERE j.kind='media_sidecar'
 AND j.payload->>'file_id'=i.file_id::text AND j.payload->>'unit_id'=i.unit_id::text
 AND j.state IN ('queued','running') ORDER BY j.created_at DESC,j.id DESC LIMIT 1
) active ON true
LEFT JOIN LATERAL (
 SELECT CASE WHEN j.state='succeeded' THEN 'complete' ELSE 'review' END AS state
 FROM job j WHERE j.kind='media_sidecar'
 AND j.payload->>'file_id'=i.file_id::text AND j.payload->>'unit_id'=i.unit_id::text
 ORDER BY j.created_at DESC,j.id DESC LIMIT 1
) latest ON true;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM media_scrape_batch) THEN
  RAISE EXCEPTION 'cannot drop media scrape command history';
 END IF;
END $$;
-- +goose StatementEnd
DROP VIEW media_scrape_candidate;
DROP INDEX media_sidecar_subject_idx;
DROP TABLE media_scrape_item;
DROP TABLE media_scrape_batch;
