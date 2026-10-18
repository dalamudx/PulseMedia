-- +goose Up
-- A linked return is not an imported file. Retain the old file and every
-- protection while recording a separately observed, atomic import.
CREATE TABLE movie_upgrade_organization_ingestion (
 operation_id UUID PRIMARY KEY REFERENCES movie_upgrade_organization_preparation(operation_id),
 job_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_organization_execution_result(job_id),
 actor_id UUID NOT NULL REFERENCES app_user(id),
 file_id UUID NOT NULL UNIQUE REFERENCES library_file(id) DEFERRABLE INITIALLY DEFERRED,
 fact_id UUID NOT NULL UNIQUE REFERENCES unit_fact(id) DEFERRABLE INITIALLY DEFERRED,
 baseline JSONB NOT NULL CHECK(jsonb_typeof(baseline)='object'),
 file_snapshot JSONB NOT NULL CHECK(jsonb_typeof(file_snapshot)='object'),
 mapping_snapshot JSONB NOT NULL CHECK(jsonb_typeof(mapping_snapshot)='object'),
 fact_snapshot JSONB NOT NULL CHECK(jsonb_typeof(fact_snapshot)='object'),
 observed_at TIMESTAMPTZ NOT NULL CHECK(isfinite(observed_at)),
 ingested_at TIMESTAMPTZ NOT NULL CHECK(isfinite(ingested_at) AND ingested_at>=observed_at AND ingested_at<observed_at+interval '5 seconds'),
 status TEXT NOT NULL CHECK(status='imported_pending_retirement'),
 execution_allowed BOOLEAN NOT NULL CHECK(NOT execution_allowed)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_ingestion_state(op UUID,newfile UUID,newfact UUID) RETURNS JSONB LANGUAGE SQL STABLE AS $$
 SELECT jsonb_build_object(
 'actor',to_jsonb(actor)-'password_hash','subscription',to_jsonb(s),'policy',to_jsonb(pol),'revision',to_jsonb(rev),
 'root',to_jsonb(root),'unit',to_jsonb(u),'media',to_jsonb(m),'client',to_jsonb(client),
 'identities',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM media_identity x WHERE x.media_id=m.id),
 'wanted',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM wanted_item x WHERE x.subscription_id=s.id),
 'files',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM library_file x WHERE x.id<>newfile AND (x.id=a.old_file_id OR x.id IN(SELECT file_id FROM library_file_unit WHERE unit_id=u.id) OR (x.root_id=root.id AND x.rel_path=p.snapshot->'physical'->>'target_rel'))),
 'facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x WHERE x.id<>newfact AND (x.unit_id=u.id OR x.source_file_id=a.old_file_id)),
 'mappings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.file_id,x.unit_id) FROM library_file_unit x WHERE x.file_id<>newfile AND (x.unit_id=u.id OR x.file_id=a.old_file_id)),
 'lanes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE x.operation_id=op),
 'reservations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE x.operation_id=op),
 'requirements',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE x.operation_id=op),
 'original',pulse_upgrade_tracking_original_digest(op),
 'preparation',to_jsonb(p),'command',to_jsonb(b),'claim',to_jsonb(c),'job',to_jsonb(j),'permit',to_jsonb(permit),'result',to_jsonb(r),
 'audits',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM audit_log x WHERE x.target_type='subscription' AND x.target_id=s.id::text AND x.action<>'upgrade.organization-ingest'),
 'events',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM outbox_event x WHERE x.event_type LIKE 'upgrade.%' AND x.event_type<>'upgrade.organization-ingest' AND x.aggregate_id IN(op::text,b.job_id::text,permit.permit_id::text,(p.snapshot->'authorization'->>'observation_id'))),
 'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM idempotency_record x WHERE x.idempotency_key LIKE 'upgrade.%' AND x.idempotency_key NOT LIKE 'upgrade.organization-ingest:%' AND x.resource_id IN(op::text,b.job_id::text,permit.permit_id::text,(p.snapshot->'authorization'->>'observation_id'))))
 FROM movie_upgrade_authority a JOIN movie_upgrade_organization_preparation p USING(operation_id)
 JOIN movie_upgrade_organization_job b USING(operation_id) JOIN movie_upgrade_organization_job_claim c USING(job_id)
 JOIN movie_upgrade_organization_execution_permit permit ON permit.job_id=b.job_id
 JOIN movie_upgrade_organization_execution_result r ON r.permit_id=permit.permit_id
 JOIN job j ON j.id=b.job_id JOIN app_user actor ON actor.id=a.actor_id
 JOIN subscription s ON s.id=a.subscription_id JOIN policy pol ON pol.id=s.quality_policy_id
 JOIN policy_revision rev ON rev.policy_id=pol.id AND rev.revision=pol.current_revision
 JOIN library_root root ON root.id=a.root_id JOIN media_unit u ON u.id=a.unit_id
 JOIN media m ON m.id=u.media_id JOIN download_client client ON client.id=a.client_id WHERE a.operation_id=op
$$;
CREATE FUNCTION pulse_upgrade_ingestion_insert() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE p movie_upgrade_organization_preparation%ROWTYPE; a movie_upgrade_organization_authorization%ROWTYPE; r movie_upgrade_organization_execution_result%ROWTYPE;
 physical JSONB; base JSONB;
BEGIN
 SELECT * INTO STRICT p FROM movie_upgrade_organization_preparation WHERE operation_id=NEW.operation_id;
 SELECT * INTO STRICT a FROM movie_upgrade_organization_authorization WHERE id=p.authorization_id;
 SELECT * INTO STRICT r FROM movie_upgrade_organization_execution_result WHERE job_id=NEW.job_id;
 physical:=p.snapshot->'physical'; base:=pulse_upgrade_ingestion_state(NEW.operation_id,NEW.file_id,NEW.fact_id);
 IF NEW.actor_id<>p.actor_id OR base IS NULL OR NEW.baseline IS DISTINCT FROM base
 OR NEW.job_id IS DISTINCT FROM (base->'command'->>'job_id')::uuid
 OR r.status<>'linked_pending_ingestion' OR NOT pulse_upgrade_organization_execution_finished(NEW.job_id,base->'job')
 OR pulse_upgrade_organization_basis(NEW.operation_id,a.observation_id) IS DISTINCT FROM a.snapshot
 OR NEW.ingested_at>clock_timestamp() OR NEW.observed_at+interval '5 seconds'<=clock_timestamp()
 OR EXISTS(SELECT 1 FROM library_file WHERE id=NEW.file_id OR (root_id=a.root_id AND rel_path=physical->>'target_rel' AND retired_at IS NULL))
 OR EXISTS(SELECT 1 FROM unit_fact WHERE id=NEW.fact_id)
 OR NEW.file_snapshot IS DISTINCT FROM to_jsonb(jsonb_populate_record(NULL::library_file,jsonb_build_object(
 'id',NEW.file_id,'root_id',a.root_id,'rel_path',physical->>'target_rel','size_bytes',(physical->'source'->>'Size')::bigint,
 'mtime_ns',(physical->'source'->>'Mtime')::bigint,'file_fingerprint','stat.v1:'||(physical->'source'->>'Inode')||':'||(physical->'source'->>'Size')||':'||(physical->'source'->>'Mtime'),
 'last_mutation_seq',(base->'root'->>'mutation_seq')::bigint+1,'mapping_revision',2,'created_at',NEW.ingested_at)))
 OR NEW.mapping_snapshot IS DISTINCT FROM to_jsonb(jsonb_populate_record(NULL::library_file_unit,jsonb_build_object('file_id',NEW.file_id,'unit_id',a.unit_id,'created_at',NEW.ingested_at)))
 OR NEW.fact_snapshot IS DISTINCT FROM to_jsonb(jsonb_populate_record(NULL::unit_fact,jsonb_build_object('id',NEW.fact_id,'unit_id',a.unit_id,'root_id',a.root_id,'stage','imported','source_file_id',NEW.file_id,'quality_profile',p.step_snapshot->'quality_profile','created_at',NEW.ingested_at)))
 THEN RAISE EXCEPTION 'upgrade ingestion binding invalid' USING ERRCODE='23514'; END IF;
 PERFORM pulse_assert_upgrade_organization_preparation(NEW.operation_id);
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_assert_upgrade_ingestion(op UUID) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE i movie_upgrade_organization_ingestion%ROWTYPE; actual JSONB; f JSONB; mapping JSONB; fact JSONB;
BEGIN
 SELECT * INTO STRICT i FROM movie_upgrade_organization_ingestion WHERE operation_id=op;
 actual:=pulse_upgrade_ingestion_state(op,i.file_id,i.fact_id);
 SELECT to_jsonb(x) INTO f FROM library_file x WHERE id=i.file_id;
 SELECT to_jsonb(x) INTO mapping FROM library_file_unit x WHERE file_id=i.file_id;
 SELECT to_jsonb(x) INTO fact FROM unit_fact x WHERE id=i.fact_id;
 IF actual IS NULL OR (actual-'root'-'unit') IS DISTINCT FROM (i.baseline-'root'-'unit')
 OR actual->'root' IS DISTINCT FROM i.baseline->'root'||jsonb_build_object('mutation_seq',(i.baseline->'root'->>'mutation_seq')::bigint+1,'updated_at',i.ingested_at)
 OR actual->'unit' IS DISTINCT FROM i.baseline->'unit'||jsonb_build_object('facts_version',(i.baseline->'unit'->>'facts_version')::bigint+2)
 OR f IS DISTINCT FROM i.file_snapshot OR mapping IS DISTINCT FROM i.mapping_snapshot OR fact IS DISTINCT FROM i.fact_snapshot
 OR (SELECT count(*) FROM library_file_unit WHERE file_id=i.file_id)<>1
 OR (SELECT count(*) FROM unit_fact WHERE source_file_id=i.file_id)<>1
 THEN RAISE EXCEPTION 'upgrade ingestion effects changed' USING ERRCODE='23514'; END IF;
 IF i.observed_at>clock_timestamp() OR i.observed_at+interval '5 seconds'<=clock_timestamp()
 THEN RAISE EXCEPTION 'upgrade ingestion observation expired' USING ERRCODE='23514'; END IF;
END $$;
CREATE FUNCTION pulse_upgrade_ingestion_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN PERFORM pulse_assert_upgrade_ingestion(NEW.operation_id); RETURN NULL; END $$;
-- +goose StatementEnd
CREATE TRIGGER upgrade_ingestion_insert BEFORE INSERT ON movie_upgrade_organization_ingestion FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_ingestion_insert();
CREATE CONSTRAINT TRIGGER upgrade_ingestion_binding AFTER INSERT ON movie_upgrade_organization_ingestion DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_ingestion_binding();
CREATE TRIGGER upgrade_ingestion_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_organization_ingestion FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
REVOKE ALL ON movie_upgrade_organization_ingestion FROM PUBLIC;

-- +goose Down
LOCK TABLE movie_upgrade_organization_ingestion,movie_upgrade_organization_execution_result,movie_upgrade_organization_execution_permit,movie_upgrade_organization_preparation,movie_upgrade_organization_job,movie_upgrade_organization_job_claim,job,app_user,subscription,policy,policy_revision,library_root,library_file,library_file_unit,unit_fact,media_unit,media,media_identity,download_client,acquisition_reservation,unit_reservation,download_requirement,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_organization_ingestion)
 OR EXISTS(SELECT 1 FROM audit_log WHERE action='upgrade.organization-ingest')
 OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type='upgrade.organization-ingest')
 OR EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.organization-ingest:%')
 THEN RAISE EXCEPTION 'upgrade ingestion history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE movie_upgrade_organization_ingestion;
DROP FUNCTION pulse_upgrade_ingestion_insert();
DROP FUNCTION pulse_upgrade_ingestion_binding();
DROP FUNCTION pulse_assert_upgrade_ingestion(UUID);
DROP FUNCTION pulse_upgrade_ingestion_state(UUID,UUID,UUID);
