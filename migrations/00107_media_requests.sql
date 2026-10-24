-- +goose Up
-- Requests record independent intent; submitting one authorizes no execution.
-- +goose StatementBegin
CREATE FUNCTION pulse_media_request_seasons_valid(p_seasons INTEGER[], p_specials BOOLEAN)
RETURNS BOOLEAN LANGUAGE SQL IMMUTABLE STRICT AS $$
 SELECT cardinality(p_seasons)<=64
    AND p_seasons=COALESCE((SELECT array_agg(DISTINCT n ORDER BY n) FROM unnest(p_seasons) AS s(n)), '{}'::integer[])
    AND NOT EXISTS(SELECT 1 FROM unnest(p_seasons) AS s(n) WHERE n IS NULL OR n<0 OR n>999 OR (n=0 AND NOT p_specials))
$$;
-- +goose StatementEnd

CREATE TABLE media_request (
 id UUID PRIMARY KEY,
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 media_type TEXT NOT NULL CHECK(media_type IN ('movie','tv')),
 tmdb_id BIGINT NOT NULL CHECK(tmdb_id>0 AND tmdb_id<=9007199254740991),
 seasons INTEGER[] NOT NULL DEFAULT '{}',
 include_specials BOOLEAN NOT NULL DEFAULT false,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','rejected','withdrawn')),
 version BIGINT NOT NULL DEFAULT 1,
 created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
 creation_key TEXT NOT NULL CHECK(creation_key COLLATE "C" ~ '^[A-Za-z0-9:_.-]{1,80}$'),
 input_hash TEXT NOT NULL CHECK(input_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 creation_receipt JSONB NOT NULL CHECK(jsonb_typeof(creation_receipt)='object'),
 decision_actor_id UUID REFERENCES app_user(id) ON DELETE RESTRICT,
 decided_at TIMESTAMPTZ,
 reason TEXT NOT NULL DEFAULT '' CHECK(char_length(reason)<=512 AND reason COLLATE "C" !~ '[[:cntrl:]]' AND reason COLLATE "C" !~ U&'[\0080-\009F]'),
 subscription_id UUID REFERENCES subscription(id) ON DELETE RESTRICT,
 decision_key TEXT CHECK(decision_key COLLATE "C" ~ '^[A-Za-z0-9:_.-]{1,80}$'),
 decision_hash TEXT CHECK(decision_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 UNIQUE(owner_id,creation_key),
 CHECK(pulse_media_request_seasons_valid(seasons,include_specials)),
 CHECK(media_type<>'movie' OR (seasons='{}'::integer[] AND NOT include_specials)),
 CHECK(updated_at>=created_at),
 CHECK((state='pending' AND version=1 AND decision_actor_id IS NULL AND decided_at IS NULL AND reason='' AND subscription_id IS NULL AND decision_key IS NULL AND decision_hash IS NULL)
    OR (state='approved' AND version=2 AND decision_actor_id IS NOT NULL AND decided_at IS NOT NULL AND reason='' AND subscription_id IS NOT NULL AND decision_key IS NOT NULL AND decision_hash IS NOT NULL)
    OR (state IN ('rejected','withdrawn') AND version=2 AND decision_actor_id IS NOT NULL AND decided_at IS NOT NULL AND subscription_id IS NULL AND decision_key IS NOT NULL AND decision_hash IS NOT NULL)),
 CHECK(state<>'withdrawn' OR decision_actor_id=owner_id)
);
CREATE INDEX media_request_owner_page ON media_request(owner_id,id DESC);
CREATE INDEX media_request_pending_page ON media_request(id DESC) WHERE state='pending';

-- +goose StatementBegin
CREATE FUNCTION pulse_media_request_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE receipt_request JSONB;
BEGIN
 IF TG_OP IN ('DELETE','TRUNCATE') THEN
  RAISE EXCEPTION 'media request history cannot be removed' USING ERRCODE='23514';
 END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'pending' OR NEW.version<>1 OR NEW.updated_at<>NEW.created_at THEN
   RAISE EXCEPTION 'media request must start pending at version one' USING ERRCODE='23514';
  END IF;
  receipt_request:=NEW.creation_receipt->'request';
  -- Compare the complete typed receipt, allowing equivalent RFC3339 timestamp spellings.
  IF NEW.creation_receipt IS DISTINCT FROM jsonb_build_object(
   'command_key',NEW.creation_key,
   'request',jsonb_build_object(
    'id',NEW.id,'owner_id',NEW.owner_id,
    'intent',jsonb_build_object('media_type',NEW.media_type,'tmdb_id',NEW.tmdb_id,'seasons',to_jsonb(NEW.seasons),'include_specials',NEW.include_specials),
    'state','pending','version',1,'created_at',receipt_request->'created_at','updated_at',receipt_request->'updated_at','reason',''))
   OR jsonb_typeof(receipt_request->'created_at') IS DISTINCT FROM 'string'
   OR jsonb_typeof(receipt_request->'updated_at') IS DISTINCT FROM 'string' THEN
   RAISE EXCEPTION 'media request creation receipt mismatch' USING ERRCODE='23514';
  END IF;
  IF (receipt_request->>'created_at')::timestamptz IS DISTINCT FROM NEW.created_at
   OR (receipt_request->>'updated_at')::timestamptz IS DISTINCT FROM NEW.updated_at THEN
   RAISE EXCEPTION 'media request creation receipt timestamp mismatch' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 IF OLD.state<>'pending' THEN
  RAISE EXCEPTION 'media request terminal history is immutable' USING ERRCODE='23514';
 END IF;
 IF ROW(NEW.id,NEW.owner_id,NEW.media_type,NEW.tmdb_id,NEW.seasons,NEW.include_specials,NEW.creation_key,NEW.input_hash,NEW.creation_receipt,NEW.created_at)
  IS DISTINCT FROM ROW(OLD.id,OLD.owner_id,OLD.media_type,OLD.tmdb_id,OLD.seasons,OLD.include_specials,OLD.creation_key,OLD.input_hash,OLD.creation_receipt,OLD.created_at) THEN
  RAISE EXCEPTION 'media request original intent is immutable' USING ERRCODE='23514';
 END IF;
 IF NEW.state NOT IN ('approved','rejected','withdrawn') OR NEW.version<>OLD.version+1 THEN
  RAISE EXCEPTION 'media request may close only once' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER media_request_guard BEFORE INSERT OR UPDATE OR DELETE ON media_request
 FOR EACH ROW EXECUTE FUNCTION pulse_media_request_guard();
CREATE TRIGGER media_request_no_truncate BEFORE TRUNCATE ON media_request
 FOR EACH STATEMENT EXECUTE FUNCTION pulse_media_request_guard();

CREATE FUNCTION pulse_media_request_approval_graph() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state<>'approved' THEN RETURN NULL; END IF;
 -- Lock the durable authorization evidence until commit; later subscription state changes are valid.
 PERFORM 1 FROM subscription WHERE id=NEW.subscription_id AND user_id=NEW.owner_id FOR SHARE;
 IF NOT FOUND THEN
  RAISE EXCEPTION 'media request approval subscription owner mismatch' USING ERRCODE='23514';
 END IF;
 PERFORM 1 FROM idempotency_record
 WHERE actor_type='user' AND actor_id=NEW.decision_actor_id::text
  AND idempotency_key='subscription.creation:media.request.approve:'||NEW.id::text||':'||NEW.decision_key
  AND request_hash=NEW.decision_hash AND resource_id=NEW.subscription_id::text AND response_code=201
  AND response_body #>> '{result,subscription,ID}'=NEW.subscription_id::text
 FOR SHARE;
 IF NOT FOUND THEN
  RAISE EXCEPTION 'media request approval requires original subscription creation receipt' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER media_request_approval_graph AFTER INSERT OR UPDATE ON media_request
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_media_request_approval_graph();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 LOCK TABLE media_request IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM media_request) THEN
  RAISE EXCEPTION 'media request history prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE media_request;
DROP FUNCTION pulse_media_request_approval_graph();
DROP FUNCTION pulse_media_request_guard();
DROP FUNCTION pulse_media_request_seasons_valid(INTEGER[],BOOLEAN);