DROP INDEX idx_job_queue_status_run_after;
CREATE INDEX idx_job_queue_status_created ON job_queue(status, created_at);

ALTER TABLE job_queue RENAME COLUMN attempts TO retry_count;
ALTER TABLE job_queue
    DROP CONSTRAINT job_queue_status_check,
    DROP COLUMN run_after,
    ALTER COLUMN retry_count DROP NOT NULL,
    ALTER COLUMN status DROP NOT NULL;

ALTER TABLE documents
    DROP CONSTRAINT documents_status_check,
    DROP COLUMN failure_reason;

DROP INDEX idx_chunks_embedding;
TRUNCATE chunks;
ALTER TABLE chunks
    ALTER COLUMN document_id DROP NOT NULL,
    ALTER COLUMN embedding DROP NOT NULL,
    ALTER COLUMN embedding TYPE vector(3584);
