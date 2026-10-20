-- name: SiteCheckinConflictingCalendar :one
SELECT EXISTS(SELECT 1 FROM site_checkin_day WHERE indexer_id=sqlc.arg(indexer_id)::uuid AND expected_account_id=sqlc.arg(expected_account_id)::text AND prepared_execution_id IS NOT NULL
 AND timezone<>sqlc.arg(timezone)::text)::boolean AS conflicting;

-- name: SiteCheckinRestoreBlocked :one
SELECT EXISTS(SELECT 1 FROM site_checkin_day d WHERE d.status<>'attended_observed' AND d.restore_id IS NULL
 AND (d.prepared_execution_id IS NOT NULL OR EXISTS(SELECT 1 FROM site_checkin_execution e WHERE e.day_id=d.id AND e.finished_at IS NULL)))::boolean AS blocked;

-- name: ListSiteCheckinSources :many
SELECT c.indexer_id FROM site_checkin_config c JOIN indexer i ON i.id=c.indexer_id
CROSS JOIN LATERAL pulse_checkin_calendar(clock_timestamp(),c.timezone,c.daily_at) cal
WHERE c.enabled AND i.config_version=c.source_config_version AND cal.due
AND NOT EXISTS(SELECT 1 FROM indexer_lifecycle l WHERE l.indexer_id=c.indexer_id AND (NOT l.enabled OR l.deleted_at IS NOT NULL))
AND NOT EXISTS(SELECT 1 FROM site_checkin_day d WHERE d.indexer_id=c.indexer_id AND d.expected_account_id=c.expected_account_id AND d.calendar_day=cal.calendar_day)
ORDER BY c.indexer_id LIMIT 64;

-- name: SiteCheckinBinding :one
SELECT pulse_checkin_binding($1::uuid)::text AS binding;

-- name: SiteCheckinClock :one
SELECT clock_timestamp()::timestamptz AS now;

-- name: SiteCheckinCalendar :one
SELECT calendar_day::date,day_start::timestamptz,day_end::timestamptz,due::boolean
FROM pulse_checkin_calendar(sqlc.arg(instant)::timestamptz,sqlc.arg(zone)::text,sqlc.arg(daily)::text);

-- name: SiteCheckinArm :one
SELECT set_config('pulse.checkin_nonce',$1::text,true)::text AS nonce;

-- name: SiteCheckinBackend :one
SELECT pid::integer,started::timestamptz FROM pulse_checkin_identity();

-- name: GetSiteCheckinDay :one
SELECT * FROM site_checkin_day WHERE id=$1 FOR UPDATE;

-- name: FindSiteCheckinDay :one
SELECT * FROM site_checkin_day WHERE original_job_id=$1 OR observation_job_id=$1;

-- name: FindSiteCheckinIdentity :one
SELECT * FROM site_checkin_day WHERE indexer_id=$1 AND expected_account_id=$2 AND calendar_day=$3;

-- name: CreateSiteCheckinDay :exec
INSERT INTO site_checkin_day(id,indexer_id,expected_account_id,profile,timezone,calendar_day,day_start,day_end,original_job_id,config_snapshot,config_version,source_version,binding_digest)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13);

-- name: CreateSiteCheckinJob :exec
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts,not_before,created_at,updated_at)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(kind)::text,(sqlc.arg(id)::uuid)::text,'queued',sqlc.arg(payload)::jsonb,sqlc.arg(max_attempts)::integer,clock_timestamp(),clock_timestamp(),clock_timestamp());

-- name: CreateSiteCheckinExecution :one
INSERT INTO site_checkin_execution(id,day_id,job_id,owner,token,nonce,backend_pid,backend_start,mode,config_version,source_version,source_fingerprint)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING *;

-- name: GetSiteCheckinExecution :one
SELECT * FROM site_checkin_execution WHERE id=$1 FOR UPDATE;

-- name: StartSiteCheckinObserve :execrows
UPDATE site_checkin_execution SET observe_started_at=clock_timestamp()
WHERE id=$1 AND state='active' AND observe_started_at IS NULL;

-- name: PrepareSiteCheckinExecution :one
UPDATE site_checkin_execution e SET state='prepared',prepared_at=clock_timestamp(),permission_id=$2,
valid_until=LEAST(d.day_end-interval '10 seconds',j.lease_until-interval '10 seconds')
FROM site_checkin_day d,job j
WHERE e.id=$1 AND d.id=e.day_id AND j.id=e.job_id AND e.mode='send' AND e.state='active'
AND e.observe_started_at IS NOT NULL AND d.prepared_execution_id IS NULL
AND clock_timestamp()>=d.day_start AND clock_timestamp()>=e.observe_started_at
AND clock_timestamp()+interval '45 seconds'<d.day_end AND clock_timestamp()+interval '25 seconds'<j.lease_until
RETURNING e.*;

-- name: MarkSiteCheckinPrepared :execrows
UPDATE site_checkin_day SET prepared_execution_id=$2,status='prepared' WHERE id=$1 AND prepared_execution_id IS NULL AND status='open';

-- name: FinishSiteCheckinExecution :execrows
UPDATE site_checkin_execution SET state=$2,result=$3,safe_code=$4,finished_at=clock_timestamp()
WHERE id=$1 AND finished_at IS NULL;

-- name: FinishSiteCheckinDay :exec
UPDATE site_checkin_day SET status=$2,safe_code=$3,observed_at=CASE WHEN $2='attended_observed' THEN clock_timestamp() ELSE NULL END
WHERE id=$1 AND status<>'attended_observed';

-- name: FinishSiteCheckinJob :execrows
UPDATE job SET state=$4,result='{}',error=NULLIF($5::text,''),finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE id=$1 AND lease_owner=$2 AND lease_token=$3 AND state='running' AND lease_until>clock_timestamp();

-- name: ListSiteCheckinRecovery :many
SELECT d.id FROM site_checkin_day d JOIN job j ON j.id=COALESCE(d.observation_job_id,d.original_job_id)
WHERE d.restore_id IS NULL AND d.status<>'attended_observed' AND (j.state='dead' OR (j.state='running' AND j.lease_until<=clock_timestamp()) OR (j.state='queued' AND j.attempts>0))
AND (d.status<>'review_required' OR d.prepared_execution_id IS NOT NULL AND d.observation_job_id IS NULL)
ORDER BY d.id LIMIT 64;

-- name: ArmSiteCheckinRecovery :one
SELECT set_config('pulse.checkin_recovery','on',true)::text AS armed;

-- name: RecoverSiteCheckinJob :exec
UPDATE job SET state=sqlc.arg(new_state)::text,lease_until=NULL,lease_owner=NULL,updated_at=clock_timestamp(),
finished_at=CASE WHEN sqlc.arg(new_state)::text='dead' THEN clock_timestamp() ELSE NULL END
WHERE id=$1 AND (state='dead' OR state='queued' OR state='running' AND lease_until<=clock_timestamp());

-- name: RecoverSiteCheckinExecutions :exec
UPDATE site_checkin_execution SET state=CASE WHEN prepared_at IS NULL THEN 'not_sent' ELSE 'review_required' END,
finished_at=clock_timestamp(),result='{}',safe_code='unavailable'
WHERE day_id=$1 AND finished_at IS NULL;

-- name: SetSiteCheckinObservationJob :execrows
UPDATE site_checkin_day SET observation_job_id=$2 WHERE id=$1 AND prepared_execution_id IS NOT NULL AND observation_job_id IS NULL;
-- name: GetSiteCheckinRestoreDay :one
SELECT * FROM site_checkin_day WHERE id=$1 FOR UPDATE;

-- name: MarkSiteCheckinRestoreDecision :execrows
UPDATE site_checkin_day SET restore_id=sqlc.arg(restore_id)::uuid,restore_operator=sqlc.arg(operator)::text,
restore_evidence=sqlc.arg(evidence)::text,restore_recorded_at=clock_timestamp(),restore_decision='retain_review'
WHERE id=sqlc.arg(day_id)::uuid AND restore_id IS NULL AND status<>'attended_observed'
AND EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated)
AND NOT EXISTS(SELECT 1 FROM job WHERE id IN (original_job_id,observation_job_id) AND state IN ('queued','running'));