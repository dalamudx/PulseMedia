-- +goose Up
ALTER TABLE complete_tv_submission_preparation
 DROP CONSTRAINT complete_tv_submission_preparation_proof_check;
ALTER TABLE complete_tv_submission_preparation
 ADD CONSTRAINT complete_tv_submission_preparation_proof_check CHECK (
  jsonb_typeof(proof)='object'
  AND COALESCE(proof->>'kind' IN ('complete_tv_prepared_v1','complete_tv_prepared_v2'),false)
  AND (proof->>'kind'='complete_tv_prepared_v1' OR COALESCE(
   jsonb_typeof(proof->'save_path')='string' AND length(proof->>'save_path')>0,false))
 );

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM complete_tv_submission_preparation WHERE proof->>'kind'='complete_tv_prepared_v2') THEN
  RAISE EXCEPTION 'cannot downgrade complete TV preparation while v2 evidence exists';
 END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE complete_tv_submission_preparation
 DROP CONSTRAINT complete_tv_submission_preparation_proof_check;
ALTER TABLE complete_tv_submission_preparation
 ADD CONSTRAINT complete_tv_submission_preparation_proof_check
 CHECK(jsonb_typeof(proof)='object' AND proof->>'kind'='complete_tv_prepared_v1');
