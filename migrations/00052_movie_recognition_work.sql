-- +goose Up
CREATE UNIQUE INDEX library_file_recognition_scope ON library_file(id,root_id);
CREATE TABLE movie_recognition_work (
 job_id UUID PRIMARY KEY REFERENCES job(id),
 file_id UUID NOT NULL,
 root_id UUID NOT NULL,
 root_generation BIGINT NOT NULL CHECK(root_generation>0),
 outcome TEXT NOT NULL DEFAULT 'pending' CHECK(outcome IN('pending','linked','review')),
 reason TEXT,
 unit_id UUID REFERENCES media_unit(id),
 finished_at TIMESTAMPTZ,
 FOREIGN KEY(file_id,root_id) REFERENCES library_file(id,root_id),
 UNIQUE(file_id,root_generation),
 CHECK((outcome='pending' AND reason IS NULL AND unit_id IS NULL AND finished_at IS NULL)
 OR (outcome='linked' AND reason IS NULL AND unit_id IS NOT NULL AND finished_at IS NOT NULL)
 OR (outcome='review' AND reason IS NOT NULL AND unit_id IS NULL AND finished_at IS NOT NULL))
);
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_recognition_work) THEN
 RAISE EXCEPTION 'movie recognition history prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE movie_recognition_work;
DROP INDEX library_file_recognition_scope;
