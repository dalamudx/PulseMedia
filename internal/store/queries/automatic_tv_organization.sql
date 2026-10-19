-- name: AutomaticTVOrganizationSchema :one
SELECT to_regclass('automatic_tv_execution_authority') IS NOT NULL AS authority,
 to_regclass('automatic_tv_submission_preparation') IS NOT NULL AS preparation,
 to_regclass('automatic_tv_organization_plan') IS NOT NULL AS organization;

-- name: AutomaticTVOrganizationOriginal :one
SELECT a.snapshot AS authority,p.proof,p.authority_hash,p.proof_hash,r.inventory,r.evidence_stamp AS inventory_stamp,
 r.job_id AS observation_job_id,j.id AS organization_job_id
FROM automatic_tv_execution_authority a
JOIN automatic_tv_submission_preparation p USING(operation_id)
JOIN automatic_tv_observation_result r ON r.operation_id=a.operation_id AND r.task_id=p.task_id AND r.complete AND NOT r.review_required
JOIN automatic_tv_observation_job b ON b.job_id=r.job_id AND b.operation_id=a.operation_id AND b.task_id=p.task_id AND b.send_attempt_id=p.physical_attempt_id
JOIN automatic_tv_physical_attempt x ON x.id=p.physical_attempt_id AND x.finished_at IS NOT NULL
JOIN job observed ON observed.id=r.job_id AND observed.state='succeeded' AND observed.lease_token=r.lease_token
JOIN job j ON j.kind='download_organization' AND j.dedupe_key='organize:'||p.task_id::text
 AND j.payload=jsonb_build_object('operation_id',a.operation_id,'task_id',p.task_id,'automatic_tv_authority',a.operation_id,'observation_job_id',r.job_id,'inventory_stamp',r.evidence_stamp)
WHERE a.operation_id=$1 AND r.authority_hash=p.authority_hash AND r.proof_hash=p.proof_hash
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_physical_attempt active WHERE active.operation_id=a.operation_id AND active.finished_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_permit active WHERE active.operation_id=a.operation_id AND active.finished_at IS NULL);

-- name: AutomaticTVOrganizationLockClient :one
SELECT * FROM download_client WHERE id=$1 FOR UPDATE;

-- name: AutomaticTVOrganizationClientSnapshot :one
SELECT to_jsonb(c)::jsonb AS snapshot FROM download_client c WHERE id=$1;

-- name: AutomaticTVOrganizationCandidateOriginal :one
SELECT EXISTS(SELECT 1 FROM automatic_tv_execution_authority a
 JOIN automatic_tv_query_candidate f ON f.query_id=a.query_id AND f.candidate_id=a.candidate_id
 JOIN release_candidate c ON c.id=a.candidate_id
 WHERE a.operation_id=$1 AND f.title=c.title AND f.guid=c.guid AND f.info_hash IS NOT DISTINCT FROM c.info_hash
 AND f.link_encrypted=c.link_encrypted AND f.quality_profile=c.quality_profile AND f.size_bytes=c.size_bytes
 AND f.source_config_version=c.source_config_version) AS valid;

-- name: AutomaticTVOrganizationProtections :one
SELECT EXISTS(SELECT 1 FROM automatic_tv_execution_authority a
 JOIN automatic_tv_submission_preparation p USING(operation_id)
 JOIN acquisition_reservation lane ON lane.operation_id=a.operation_id AND lane.unit_id=a.unit_id AND lane.released_at IS NULL
 JOIN unit_reservation r ON r.operation_id=a.operation_id AND r.unit_id=a.unit_id AND r.root_id=a.root_id AND r.state='dispatched'
 JOIN download_requirement d ON d.operation_id=a.operation_id AND d.unit_id=a.unit_id AND d.root_id=a.root_id AND d.state='pending'
 WHERE a.operation_id=sqlc.arg(operation_id)::uuid AND lane.acquisition_lane='default' AND NOT lane.legacy_conflict
 AND lane.source_root_id=a.root_id AND lane.source_generation=(a.snapshot->'target'->>'root_generation')::bigint
 AND lane.source_stamp=a.snapshot->>'source_stamp' AND lane.candidate_id=a.candidate_id AND lane.task_id=p.task_id
 AND r.candidate_id=a.candidate_id AND r.purpose='acquire'
 AND d.actor_type='system' AND d.actor_id=a.round_id::text AND d.submission_fence=sqlc.arg(fence)::jsonb
 AND (SELECT count(*) FROM acquisition_reservation z WHERE z.operation_id=a.operation_id AND z.released_at IS NULL)=1
 AND (SELECT count(*) FROM unit_reservation z WHERE z.operation_id=a.operation_id AND z.state IN ('reserved','dispatched'))=1
 AND (SELECT count(*) FROM download_requirement z WHERE z.operation_id=a.operation_id AND z.state='pending')=1
 AND NOT EXISTS(SELECT 1 FROM acquisition_reservation z WHERE z.unit_id=a.unit_id AND z.released_at IS NULL AND z.operation_id<>a.operation_id)
 AND NOT EXISTS(SELECT 1 FROM unit_reservation z WHERE z.unit_id=a.unit_id AND z.state IN ('reserved','dispatched') AND z.operation_id<>a.operation_id)
 AND NOT EXISTS(SELECT 1 FROM download_requirement z WHERE z.unit_id=a.unit_id AND z.state='pending' AND z.operation_id<>a.operation_id)
 AND NOT EXISTS(SELECT 1 FROM library_file_unit u JOIN library_file f ON f.id=u.file_id WHERE u.unit_id=a.unit_id AND u.retired_at IS NULL AND f.retired_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM unit_fact z WHERE z.unit_id=a.unit_id AND z.revoked_at IS NULL AND z.stage IN ('downloaded','imported'))
 AND NOT EXISTS(SELECT 1 FROM library_reuse_unit z WHERE z.unit_id=a.unit_id AND z.active)) AS valid;

-- name: AutomaticTVOrganizationPlan :one
SELECT * FROM automatic_tv_organization_plan WHERE operation_id=$1;

-- name: AutomaticTVOrganizationInsertPlan :exec
INSERT INTO automatic_tv_organization_plan(plan_id,operation_id,task_id,observation_job_id,organization_job_id,step_id,
 authority_hash,proof_hash,inventory_stamp,source_root,source_rel,source_directory,source_root_device,source_root_inode,
 source_directory_device,source_directory_inode,source_device,source_inode,source_size,source_mtime,
 root_id,root_path,root_generation,root_device,root_inode,baseline_device,baseline_inode,
 marker_filename,marker_device,marker_inode,marker_size,marker_mtime,quality_profile,target_evidence,observed_at,client_snapshot)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36);

-- name: AutomaticTVOrganizationInsertObservation :exec
INSERT INTO automatic_tv_organization_observation(id,plan_id,step_id,execution_owner,execution_token,job_id,lease_owner,lease_token,observed_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: AutomaticTVOrganizationObservation :one
SELECT * FROM automatic_tv_organization_observation WHERE plan_id=$1 AND execution_owner=$2 AND execution_token=$3
 AND job_id=$4 AND lease_owner=$5 AND lease_token=$6 ORDER BY observed_at DESC,id DESC LIMIT 1;

-- name: AutomaticTVOrganizationInsertCompletion :exec
INSERT INTO automatic_tv_organization_completion(plan_id,step_id,file_id,job_id,lease_owner,lease_token,lease_until,plan_lease_until,observation_id,result)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10);

-- name: AutomaticTVOrganizationCompletion :one
SELECT * FROM automatic_tv_organization_completion WHERE plan_id=$1;

-- name: AutomaticTVOrganizationCompletionValid :one
SELECT automatic_tv_organization_completed($1::uuid)::boolean AS valid;

-- name: AutomaticTVOrganizationTail :one
SELECT c.lease_until>clock_timestamp() AND c.plan_lease_until>clock_timestamp()
 AND o.observed_at<=clock_timestamp() AND o.observed_at>clock_timestamp()-interval '5 seconds'
 AND automatic_tv_organization_completed(c.plan_id) AS valid
FROM automatic_tv_organization_completion c JOIN automatic_tv_organization_observation o ON o.id=c.observation_id
WHERE c.plan_id=$1;
