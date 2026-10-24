-- This is read-only ordinary-task eligibility, never removal authorization.
-- Specialized TV/upgrade histories use different completion ledgers and cannot
-- enter through this ordinary download-organization proof.
-- name: ReadOrdinarySeedingProtection :one
SELECT t.id AS task_id,
 EXISTS (
  SELECT 1 FROM download_task_client_binding b JOIN job j ON j.id=b.job_id
  WHERE b.task_id=t.id AND b.client_id=t.client_id
   AND b.client_stamp=sqlc.arg(client_stamp)::text
   AND b.client_stamp=pulse_global_rates_binding(t.client_id)
   AND j.state='succeeded' AND j.kind IN ('download_submission','episode_download_submission')
   AND j.payload->>'operation_id'=t.operation_id::text
   AND j.payload->>'candidate_id'=t.candidate_id::text
 )::boolean AS owned,
 (
  t.state='completed' AND t.organization_state='completed' AND t.total_bytes>0
  AND t.info_hash COLLATE "C" ~ '^[0-9a-f]{40}$'
  AND EXISTS (SELECT 1 FROM transfer_plan p WHERE p.id=t.transfer_plan_id
   AND p.operation_id=t.operation_id AND p.state='completed')
  AND EXISTS (SELECT 1 FROM job j WHERE j.kind='download_organization' AND j.state='succeeded'
   AND j.result->>'task_id'=t.id::text AND j.result->>'plan_id'=t.transfer_plan_id::text)
  AND EXISTS (SELECT 1 FROM download_requirement d WHERE d.operation_id=t.operation_id AND d.state='imported')
  AND NOT EXISTS (
   SELECT 1 FROM download_requirement d WHERE d.operation_id=t.operation_id AND d.state='imported'
   AND NOT EXISTS (
    SELECT 1 FROM transfer_plan p JOIN transfer_step s ON s.plan_id=p.id
    JOIN library_file f ON f.id=s.target_file_id AND f.root_id=p.root_id AND f.retired_at IS NULL
    JOIN library_file_unit m ON m.file_id=f.id AND m.unit_id=s.unit_id AND m.retired_at IS NULL
    JOIN library_root r ON r.id=p.root_id AND r.health='online' AND r.path=p.root_path AND r.generation=p.root_generation
    JOIN unit_fact fact ON fact.unit_id=s.unit_id AND fact.root_id=p.root_id
     AND fact.source_file_id=f.id AND fact.stage='imported' AND fact.revoked_at IS NULL
     AND (fact.expires_at IS NULL OR fact.expires_at>clock_timestamp())
    WHERE p.operation_id=t.operation_id AND p.root_id=d.root_id AND p.state='completed'
     AND s.unit_id=d.unit_id AND s.state='completed' AND s.source_size>0 AND f.size_bytes=s.source_size
   )
  )
  AND NOT EXISTS (SELECT 1 FROM transfer_plan p JOIN transfer_step s ON s.plan_id=p.id
   WHERE p.operation_id=t.operation_id AND (p.state<>'completed' OR s.state<>'completed'))
 )::boolean AS imported,
 (
  EXISTS (SELECT 1 FROM download_requirement d WHERE d.operation_id=t.operation_id AND d.state='pending')
  OR EXISTS (SELECT 1 FROM unit_reservation r WHERE r.operation_id=t.operation_id AND r.state IN ('reserved','dispatched'))
  OR EXISTS (SELECT 1 FROM acquisition_reservation a WHERE a.operation_id=t.operation_id AND a.released_at IS NULL)
  OR EXISTS (SELECT 1 FROM transfer_plan p WHERE p.operation_id=t.operation_id AND p.state<>'completed')
  OR EXISTS (SELECT 1 FROM job j WHERE j.kind IN ('download_submission','episode_download_submission','download_organization')
    AND j.state IN ('queued','running') AND (j.payload->>'operation_id'=t.operation_id::text OR j.payload->>'task_id'=t.id::text))
 )::boolean AS pending_work
FROM download_task t WHERE t.id=sqlc.arg(task_id)::uuid;
