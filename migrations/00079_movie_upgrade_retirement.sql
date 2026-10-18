-- +goose Up
-- Explicit retirement is separate from ingestion. A committed permit is never
-- reclaimed or replayed, including after connection loss or lease expiry.
CREATE TABLE movie_upgrade_retirement_command (
 operation_id UUID PRIMARY KEY REFERENCES movie_upgrade_organization_ingestion(operation_id),
 command_id UUID NOT NULL UNIQUE,
 actor_id UUID NOT NULL REFERENCES app_user(id),
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 160),
 input_stamp TEXT NOT NULL CHECK(input_stamp ~ '^[0-9a-f]{64}$'),
 baseline JSONB NOT NULL CHECK(jsonb_typeof(baseline)='object'),
 recycle_rel TEXT NOT NULL,
 commanded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(actor_id,request_key)
);
CREATE TABLE movie_upgrade_retirement_permit (
 operation_id UUID PRIMARY KEY REFERENCES movie_upgrade_retirement_command(operation_id),
 permit_id UUID NOT NULL UNIQUE,
 owner UUID NOT NULL,
 token BIGINT NOT NULL CHECK(token=1),
 started_at TIMESTAMPTZ NOT NULL,
 lease_until TIMESTAMPTZ NOT NULL CHECK(lease_until>started_at AND lease_until<=started_at+interval '60 seconds')
);
CREATE TABLE movie_upgrade_retirement_result (
 operation_id UUID PRIMARY KEY REFERENCES movie_upgrade_retirement_command(operation_id),
 permit_id UUID UNIQUE REFERENCES movie_upgrade_retirement_permit(permit_id),
 status TEXT NOT NULL CHECK(status IN ('retired','manual_review')),
 baseline JSONB NOT NULL CHECK(jsonb_typeof(baseline)='object'),
 observed_at TIMESTAMPTZ NOT NULL,
 recorded_at TIMESTAMPTZ NOT NULL,
 retained_until TIMESTAMPTZ,
 CHECK(recorded_at>=observed_at AND isfinite(recorded_at) AND isfinite(observed_at)),
 CHECK((status='retired' AND permit_id IS NOT NULL AND retained_until=recorded_at+interval '7 days') OR (status='manual_review' AND retained_until IS NULL))
);
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_retirement_state(op UUID) RETURNS JSONB LANGUAGE SQL STABLE AS $$
 SELECT jsonb_build_object(
 'authority',to_jsonb(a),'ingestion',to_jsonb(i),'preparation',to_jsonb(p),
 'actor',to_jsonb(actor)-'password_hash','subscription',to_jsonb(s),'policy',to_jsonb(pol),'revision',to_jsonb(rev),
 'root',to_jsonb(root)-'updated_at','unit',to_jsonb(u),'media',to_jsonb(m),
 'identities',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM media_identity x WHERE x.media_id=m.id),
 'wanted',(SELECT jsonb_agg(jsonb_build_object('id',x.id,'unit_id',x.unit_id,'intent_version',x.intent_version,'is_skipped',x.is_skipped,'user_skipped',x.user_skipped,'scope_excluded',x.scope_excluded) ORDER BY x.id) FROM wanted_item x WHERE x.subscription_id=s.id),
 'old_file',to_jsonb(oldfile),'new_file',to_jsonb(newfile),
 'old_facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x WHERE x.source_file_id=a.old_file_id),
 'new_facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x WHERE x.source_file_id=i.file_id),
 'old_mappings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.unit_id) FROM library_file_unit x WHERE x.file_id=a.old_file_id),
 'new_mappings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.unit_id) FROM library_file_unit x WHERE x.file_id=i.file_id),
 'lanes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE x.operation_id=op),
 'reservations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE x.operation_id=op),
 'requirements',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE x.operation_id=op),
 'audits',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM audit_log x WHERE x.target_type='subscription' AND x.target_id=s.id::text AND x.action NOT LIKE 'upgrade.retirement-%'),
 'events',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM outbox_event x WHERE x.event_type LIKE 'upgrade.%' AND x.event_type NOT LIKE 'upgrade.retirement-%' AND x.aggregate_id IN(op::text,i.job_id::text)),
 'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM idempotency_record x WHERE x.idempotency_key LIKE 'upgrade.%' AND x.idempotency_key NOT LIKE 'upgrade.retirement-%' AND x.resource_id IN(op::text,i.job_id::text)))
 FROM movie_upgrade_authority a JOIN movie_upgrade_organization_ingestion i USING(operation_id)
 JOIN movie_upgrade_organization_preparation p USING(operation_id)
 JOIN app_user actor ON actor.id=a.actor_id JOIN subscription s ON s.id=a.subscription_id
 JOIN policy pol ON pol.id=s.quality_policy_id JOIN policy_revision rev ON rev.policy_id=pol.id AND rev.revision=pol.current_revision
 JOIN library_root root ON root.id=a.root_id JOIN media_unit u ON u.id=a.unit_id JOIN media m ON m.id=u.media_id
 JOIN library_file oldfile ON oldfile.id=a.old_file_id JOIN library_file newfile ON newfile.id=i.file_id WHERE a.operation_id=op
$$;
CREATE FUNCTION pulse_upgrade_retirement_valid(op UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE(actor.is_active AND actor.role='admin' AND s.state='active' AND s.upgrade_mode='stop_at_cutoff'
 AND s.intent_version=a.intent_version AND s.root_id=a.root_id AND s.quality_policy_id=a.policy_id AND pol.current_revision=a.policy_revision
 AND root.generation=a.root_generation AND root.health='online' AND root.baseline_completed AND NOT u.retired
 AND oldfile.retired_at IS NULL AND oldfile.root_id=a.root_id
 AND to_jsonb(oldfile)=(SELECT x FROM jsonb_array_elements(i.baseline->'files') x WHERE x->>'id'=a.old_file_id::text)
 AND to_jsonb(oldfact)=(SELECT x FROM jsonb_array_elements(i.baseline->'facts') x WHERE x->>'id'=a.old_fact_id::text)
 AND oldfact.revoked_at IS NULL AND (oldfact.expires_at IS NULL OR oldfact.expires_at>clock_timestamp())
 AND newfact.revoked_at IS NULL AND (newfact.expires_at IS NULL OR newfact.expires_at>clock_timestamp())
 AND to_jsonb(newfile)=i.file_snapshot AND to_jsonb(newfact)=i.fact_snapshot
 AND (SELECT count(*) FROM library_file_unit WHERE file_id=a.old_file_id AND retired_at IS NULL)=1
 AND EXISTS(SELECT 1 FROM library_file_unit WHERE file_id=a.old_file_id AND retired_at IS NULL AND unit_id=a.unit_id)
 AND (SELECT count(*) FROM library_file_unit WHERE file_id=i.file_id)=1
 AND EXISTS(SELECT 1 FROM library_file_unit x WHERE file_id=i.file_id AND to_jsonb(x)=i.mapping_snapshot)
 AND NOT EXISTS(SELECT 1 FROM unit_fact x WHERE x.source_file_id IN(a.old_file_id,i.file_id) AND x.unit_id<>a.unit_id AND x.revoked_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM wanted_item w WHERE w.subscription_id=s.id AND (w.is_skipped OR w.user_skipped OR w.scope_excluded))
 AND (SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE x.operation_id=op)=i.baseline->'lanes'
 AND (SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE x.operation_id=op)=i.baseline->'reservations'
 AND (SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE x.operation_id=op)=i.baseline->'requirements',false)
 FROM movie_upgrade_authority a JOIN movie_upgrade_organization_ingestion i USING(operation_id)
 JOIN app_user actor ON actor.id=a.actor_id JOIN subscription s ON s.id=a.subscription_id JOIN policy pol ON pol.id=s.quality_policy_id
 JOIN library_root root ON root.id=a.root_id JOIN media_unit u ON u.id=a.unit_id
 JOIN library_file oldfile ON oldfile.id=a.old_file_id JOIN library_file newfile ON newfile.id=i.file_id
 JOIN unit_fact oldfact ON oldfact.id=a.old_fact_id JOIN unit_fact newfact ON newfact.id=i.fact_id WHERE a.operation_id=op
$$;
CREATE FUNCTION pulse_upgrade_retirement_command_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE b JSONB;
BEGIN
 b:=pulse_upgrade_retirement_state(NEW.operation_id);
 IF b IS NULL OR NOT pulse_upgrade_retirement_valid(NEW.operation_id) OR NEW.baseline IS DISTINCT FROM b
 OR NEW.actor_id::text IS DISTINCT FROM b->'authority'->>'actor_id'
 OR NEW.recycle_rel IS DISTINCT FROM '.pulsemedia_recycle/'||NEW.operation_id::text||'/'||(b->'old_file'->>'rel_path')
 OR NEW.input_stamp IS DISTINCT FROM encode(sha256(convert_to(jsonb_build_array('upgrade.retirement.v1',b)::text,'UTF8')),'hex')
 THEN RAISE EXCEPTION 'upgrade retirement command changed' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_upgrade_retirement_permit_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE c movie_upgrade_retirement_command%ROWTYPE;
BEGIN
 SELECT * INTO STRICT c FROM movie_upgrade_retirement_command WHERE operation_id=NEW.operation_id FOR UPDATE;
 IF NOT pulse_upgrade_retirement_valid(NEW.operation_id) OR c.baseline IS DISTINCT FROM pulse_upgrade_retirement_state(NEW.operation_id)
 OR NEW.started_at>clock_timestamp() OR NEW.lease_until<=clock_timestamp()
 OR EXISTS(SELECT 1 FROM movie_upgrade_retirement_result WHERE operation_id=NEW.operation_id)
 THEN RAISE EXCEPTION 'upgrade retirement permit invalid' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_assert_upgrade_retirement_result(op UUID) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE r movie_upgrade_retirement_result%ROWTYPE; c movie_upgrade_retirement_command%ROWTYPE; p movie_upgrade_retirement_permit%ROWTYPE; actual JSONB; expected JSONB; oldfacts JSONB; n TIMESTAMPTZ;
BEGIN
 SELECT * INTO STRICT r FROM movie_upgrade_retirement_result WHERE operation_id=op;
 SELECT * INTO STRICT c FROM movie_upgrade_retirement_command WHERE operation_id=op;
 actual:=pulse_upgrade_retirement_state(op); n:=r.recorded_at;
 IF actual IS NULL OR r.observed_at>clock_timestamp() OR r.observed_at+interval '5 seconds'<=clock_timestamp()
 THEN RAISE EXCEPTION 'upgrade retirement observation expired' USING ERRCODE='23514'; END IF;
 IF r.status='manual_review' THEN
  IF actual IS DISTINCT FROM r.baseline THEN RAISE EXCEPTION 'upgrade retirement rejection changed business state' USING ERRCODE='23514'; END IF;
  RETURN;
 END IF;
 SELECT * INTO STRICT p FROM movie_upgrade_retirement_permit WHERE operation_id=op AND permit_id=r.permit_id;
 IF p.lease_until<=clock_timestamp() OR r.baseline IS DISTINCT FROM c.baseline
 THEN RAISE EXCEPTION 'upgrade retirement execution changed' USING ERRCODE='23514'; END IF;
 SELECT jsonb_agg(CASE WHEN x->>'revoked_at' IS NULL THEN x||jsonb_build_object('revoked_at',n,'revocation_reason','quality_upgrade') ELSE x END ORDER BY x->>'id') INTO oldfacts FROM jsonb_array_elements(r.baseline->'old_facts') x;
 expected:=r.baseline||jsonb_build_object(
 'old_file',r.baseline->'old_file'||jsonb_build_object('retired_at',n,'last_mutation_seq',(r.baseline->'root'->>'mutation_seq')::bigint+1),
 'old_facts',oldfacts,
 'root',r.baseline->'root'||jsonb_build_object('mutation_seq',(r.baseline->'root'->>'mutation_seq')::bigint+1),
 'unit',r.baseline->'unit'||jsonb_build_object('facts_version',(r.baseline->'unit'->>'facts_version')::bigint+4),
 'lanes',(SELECT jsonb_agg(x||jsonb_build_object('released_at',n,'updated_at',n) ORDER BY x->>'id') FROM jsonb_array_elements(r.baseline->'lanes') x),
 'reservations',(SELECT jsonb_agg(x||jsonb_build_object('state','consumed','updated_at',n) ORDER BY x->>'id') FROM jsonb_array_elements(r.baseline->'reservations') x),
 'requirements',(SELECT jsonb_agg(x||jsonb_build_object('state','imported') ORDER BY x->>'id') FROM jsonb_array_elements(r.baseline->'requirements') x));
 IF actual IS DISTINCT FROM expected THEN RAISE EXCEPTION 'upgrade retirement effects changed' USING ERRCODE='23514'; END IF;
END $$;
CREATE FUNCTION pulse_upgrade_retirement_result_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.baseline IS DISTINCT FROM pulse_upgrade_retirement_state(NEW.operation_id) OR NEW.recorded_at>clock_timestamp()
 OR (NEW.status='retired' AND (NOT pulse_upgrade_retirement_valid(NEW.operation_id) OR NOT EXISTS(SELECT 1 FROM movie_upgrade_retirement_permit p WHERE p.operation_id=NEW.operation_id AND p.permit_id=NEW.permit_id AND p.lease_until>clock_timestamp())))
 THEN RAISE EXCEPTION 'upgrade retirement result invalid' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_upgrade_retirement_command_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.baseline IS DISTINCT FROM pulse_upgrade_retirement_state(NEW.operation_id) OR NOT pulse_upgrade_retirement_valid(NEW.operation_id)
 THEN RAISE EXCEPTION 'upgrade retirement command changed at commit' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE FUNCTION pulse_upgrade_retirement_permit_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE c movie_upgrade_retirement_command%ROWTYPE;
BEGIN
 SELECT * INTO STRICT c FROM movie_upgrade_retirement_command WHERE operation_id=NEW.operation_id;
 IF c.baseline IS DISTINCT FROM pulse_upgrade_retirement_state(NEW.operation_id) OR NOT pulse_upgrade_retirement_valid(NEW.operation_id) OR NEW.lease_until<=clock_timestamp()
 THEN RAISE EXCEPTION 'upgrade retirement permit changed at commit' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE FUNCTION pulse_upgrade_retirement_result_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN PERFORM pulse_assert_upgrade_retirement_result(NEW.operation_id); RETURN NULL; END $$;
-- +goose StatementEnd
CREATE TRIGGER upgrade_retirement_command_guard BEFORE INSERT ON movie_upgrade_retirement_command FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_retirement_command_guard();
CREATE CONSTRAINT TRIGGER upgrade_retirement_command_binding AFTER INSERT ON movie_upgrade_retirement_command DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_retirement_command_binding();
CREATE TRIGGER upgrade_retirement_permit_guard BEFORE INSERT ON movie_upgrade_retirement_permit FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_retirement_permit_guard();
CREATE CONSTRAINT TRIGGER upgrade_retirement_permit_binding AFTER INSERT ON movie_upgrade_retirement_permit DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_retirement_permit_binding();
CREATE TRIGGER upgrade_retirement_result_guard BEFORE INSERT ON movie_upgrade_retirement_result FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_retirement_result_guard();
CREATE CONSTRAINT TRIGGER upgrade_retirement_result_binding AFTER INSERT ON movie_upgrade_retirement_result DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_retirement_result_binding();
CREATE TRIGGER upgrade_retirement_command_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_retirement_command FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE TRIGGER upgrade_retirement_permit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_retirement_permit FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE TRIGGER upgrade_retirement_result_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_retirement_result FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
REVOKE ALL ON movie_upgrade_retirement_command,movie_upgrade_retirement_permit,movie_upgrade_retirement_result FROM PUBLIC;

-- +goose Down
LOCK TABLE movie_upgrade_retirement_command,movie_upgrade_retirement_permit,movie_upgrade_retirement_result,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_retirement_command) OR EXISTS(SELECT 1 FROM movie_upgrade_retirement_permit) OR EXISTS(SELECT 1 FROM movie_upgrade_retirement_result)
 OR EXISTS(SELECT 1 FROM audit_log WHERE action LIKE 'upgrade.retirement-%') OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type LIKE 'upgrade.retirement-%')
 OR EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.retirement-%')
 THEN RAISE EXCEPTION 'upgrade retirement history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE movie_upgrade_retirement_result,movie_upgrade_retirement_permit,movie_upgrade_retirement_command;
DROP FUNCTION pulse_upgrade_retirement_result_binding();
DROP FUNCTION pulse_upgrade_retirement_permit_binding();
DROP FUNCTION pulse_upgrade_retirement_command_binding();
DROP FUNCTION pulse_upgrade_retirement_result_guard();
DROP FUNCTION pulse_assert_upgrade_retirement_result(UUID);
DROP FUNCTION pulse_upgrade_retirement_permit_guard();
DROP FUNCTION pulse_upgrade_retirement_command_guard();
DROP FUNCTION pulse_upgrade_retirement_valid(UUID);
DROP FUNCTION pulse_upgrade_retirement_state(UUID);
