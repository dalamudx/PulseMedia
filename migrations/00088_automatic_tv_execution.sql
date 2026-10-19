-- +goose Up
CREATE TABLE automatic_tv_execution_authority (
 operation_id UUID PRIMARY KEY,
 round_id UUID NOT NULL UNIQUE REFERENCES automatic_tv_round(id),
 owner_id UUID NOT NULL REFERENCES app_user(id),
 subscription_id UUID NOT NULL REFERENCES subscription(id),
 unit_id UUID NOT NULL REFERENCES media_unit(id),
 root_id UUID NOT NULL REFERENCES library_root(id),
 candidate_id UUID NOT NULL REFERENCES release_candidate(id),
 indexer_id UUID NOT NULL REFERENCES indexer(id),
 client_id UUID NOT NULL REFERENCES download_client(id),
 query_id UUID NOT NULL REFERENCES automatic_tv_query(query_id),
 snapshot JSONB NOT NULL,
 decision_stamp TEXT NOT NULL CHECK(decision_stamp ~ '^[0-9a-f]{64}$'),
 CHECK(COALESCE(jsonb_typeof(snapshot)='object'
  AND snapshot-ARRAY['schema_version','kind','operation_id','round_id','round_snapshot_hash','target','root_path_digest','query_id','query_parameters_hash','candidate_id','indexer_id','source_config_version','source_stamp','decision_stamp','issued_at','send_valid_until']='{}'::jsonb
  AND snapshot->>'schema_version'='automatic-tv-execution-v1'
  AND snapshot->>'kind'='automatic_standard_single_episode_v1'
  AND snapshot->>'operation_id'=operation_id::text
  AND snapshot->>'round_id'=round_id::text
  AND snapshot->>'candidate_id'=candidate_id::text
  AND snapshot->>'indexer_id'=indexer_id::text
  AND snapshot->>'query_id'=query_id::text
  AND snapshot->>'decision_stamp'=decision_stamp
  AND snapshot->>'round_snapshot_hash' ~ '^[0-9a-f]{64}$'
  AND snapshot->>'root_path_digest' ~ '^[0-9a-f]{64}$'
  AND snapshot->>'query_parameters_hash' ~ '^[0-9a-f]{64}$'
  AND snapshot->>'source_stamp' ~ '^[0-9a-f]{64}$'
  AND (snapshot->>'source_config_version')::bigint>0
  AND snapshot->'target'->>'schema_version'='automatic-tv-round-v1'
  AND snapshot->'target'->>'owner_id'=owner_id::text
  AND snapshot->'target'->>'subscription_id'=subscription_id::text
  AND snapshot->'target'->>'unit_id'=unit_id::text
  AND snapshot->'target'->>'root_id'=root_id::text
  AND snapshot->'target'->>'client_id'=client_id::text
  AND snapshot->'target'->>'mapping_mode'='standard'
  AND (snapshot->'target'->>'season')::integer>0
  AND (snapshot->'target'->>'episode_id')::bigint>0
  AND (snapshot->>'send_valid_until')::timestamptz=(snapshot->>'issued_at')::timestamptz+interval '5 minutes',false))
);
CREATE TABLE automatic_tv_decision (
 round_id UUID PRIMARY KEY REFERENCES automatic_tv_round(id),
 finalize_job_id UUID NOT NULL UNIQUE REFERENCES job(id),
 lease_owner TEXT NOT NULL CHECK(lease_owner<>''),
 lease_token BIGINT NOT NULL CHECK(lease_token>0),
 operation_id UUID UNIQUE REFERENCES automatic_tv_execution_authority(operation_id),
 candidate_id UUID REFERENCES release_candidate(id),
 state TEXT NOT NULL CHECK(state IN ('closed','reserved')),
 best_score INTEGER NOT NULL,
 decision JSONB NOT NULL,
 CHECK((state='closed' AND operation_id IS NULL AND candidate_id IS NULL AND best_score=-1)
  OR (state='reserved' AND operation_id IS NOT NULL AND candidate_id IS NOT NULL AND best_score>0)),
 CHECK(COALESCE(decision=jsonb_build_object('RoundID',round_id,'OperationID',COALESCE(operation_id,'00000000-0000-0000-0000-000000000000'::uuid),'State',state,'SelectedCandidateID',candidate_id,'BestScore',best_score,'Reason',decision->>'Reason') AND ((state='reserved' AND decision->>'Reason'='selected') OR (state='closed' AND decision->>'Reason' IN ('no_qualified_candidate','owner_revoked','target_changed','retry_exhausted'))),false))
);
-- +goose StatementBegin
CREATE FUNCTION guard_automatic_tv_execution_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'automatic TV execution authority and decision are immutable';
END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_authority_history BEFORE UPDATE OR DELETE ON automatic_tv_execution_authority FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_execution_history();
CREATE TRIGGER automatic_tv_authority_truncate BEFORE TRUNCATE ON automatic_tv_execution_authority FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_execution_history();
CREATE TRIGGER automatic_tv_decision_history BEFORE UPDATE OR DELETE ON automatic_tv_decision FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_execution_history();
CREATE TRIGGER automatic_tv_decision_truncate BEFORE TRUNCATE ON automatic_tv_decision FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_execution_history();
-- +goose StatementBegin
CREATE FUNCTION check_automatic_tv_execution_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a automatic_tv_execution_authority; r automatic_tv_round; d automatic_tv_decision;
BEGIN
 SELECT * INTO r FROM automatic_tv_round WHERE id=NEW.round_id;
 SELECT * INTO d FROM automatic_tv_decision WHERE round_id=NEW.round_id;
 SELECT * INTO a FROM automatic_tv_execution_authority WHERE round_id=NEW.round_id;
 IF r.state<>'closed' OR d.round_id IS NULL OR d.finalize_job_id<>r.finalize_job_id
  OR NOT EXISTS(SELECT 1 FROM job WHERE id=d.finalize_job_id AND kind='automatic_tv_finalize' AND state IN ('succeeded','dead') AND (d.state='closed' OR state='succeeded') AND lease_token=d.lease_token AND payload=jsonb_build_object('round_id',r.id) AND result=d.decision)
 THEN RAISE EXCEPTION 'automatic TV decision requires original completed finalizer'; END IF;
 IF d.state='closed' THEN
  IF a.operation_id IS NOT NULL THEN RAISE EXCEPTION 'closed decision cannot carry execution authority'; END IF;
  RETURN NULL;
 END IF;
 IF a.operation_id IS NULL OR a.operation_id<>d.operation_id OR a.candidate_id<>d.candidate_id
  OR a.subscription_id<>r.subscription_id OR a.owner_id<>r.owner_id
  OR a.snapshot->'target' IS DISTINCT FROM r.snapshot OR a.snapshot->>'round_snapshot_hash'<>r.snapshot_hash
  OR a.snapshot->>'root_path_digest' IS DISTINCT FROM (SELECT encode(sha256(convert_to(path,'UTF8')),'hex') FROM library_root WHERE id=a.root_id)
  OR NOT EXISTS(SELECT 1 FROM automatic_tv_query q JOIN automatic_tv_source s USING(round_id,indexer_id)
   JOIN automatic_tv_query_candidate c ON c.query_id=q.query_id
   WHERE q.query_id=a.query_id AND q.round_id=r.id AND q.indexer_id=a.indexer_id AND c.candidate_id=a.candidate_id
    AND q.parameters_hash=a.snapshot->>'query_parameters_hash' AND c.source_stamp=a.snapshot->>'source_stamp'
    AND s.config_version=(a.snapshot->>'source_config_version')::bigint)
 THEN RAISE EXCEPTION 'automatic TV authority requires original round proof'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER automatic_tv_authority_binding AFTER INSERT ON automatic_tv_execution_authority DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_execution_binding();
CREATE CONSTRAINT TRIGGER automatic_tv_decision_binding AFTER INSERT ON automatic_tv_decision DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_execution_binding();

-- +goose Down
LOCK TABLE automatic_tv_execution_authority,automatic_tv_decision,job,download_requirement,unit_reservation,acquisition_reservation,audit_log,outbox_event IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM automatic_tv_execution_authority) OR EXISTS(SELECT 1 FROM automatic_tv_decision)
  OR EXISTS(SELECT 1 FROM job WHERE kind='automatic_tv_download_submission' OR payload ? 'automatic_tv_authority')
  OR EXISTS(SELECT 1 FROM download_requirement WHERE submission_fence->>'kind'='automatic_tv_episode_v1' OR submission_fence ? 'automatic_tv_authority')
  OR EXISTS(SELECT 1 FROM audit_log WHERE action='automatic_tv.finalized')
  OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type='automatic_tv.finalized')
 THEN RAISE EXCEPTION 'automatic TV execution history downgrade refused'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE automatic_tv_decision,automatic_tv_execution_authority;
DROP FUNCTION check_automatic_tv_execution_binding();
DROP FUNCTION guard_automatic_tv_execution_history();
