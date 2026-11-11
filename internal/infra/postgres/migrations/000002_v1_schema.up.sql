-- Embeddings move to qwen3-embedding:0.6b (1024 dims, ADR-0001). Vectors from
-- the old 3584-dim model are meaningless under the new one, so existing Chunks
-- are dropped; their Documents must be ingested again.
TRUNCATE chunks;
ALTER TABLE chunks
    ALTER COLUMN embedding TYPE vector(1024),
    ALTER COLUMN embedding SET NOT NULL,
    ALTER COLUMN document_id SET NOT NULL;
CREATE INDEX idx_chunks_embedding ON chunks USING hnsw (embedding vector_cosine_ops);

ALTER TABLE documents
    ADD COLUMN failure_reason TEXT,
    ADD CONSTRAINT documents_status_check
        CHECK (status IN ('PENDING', 'INGESTING', 'READY', 'FAILED'));

ALTER TABLE job_queue
    ALTER COLUMN status SET NOT NULL,
    ALTER COLUMN retry_count SET NOT NULL,
    ADD COLUMN run_after TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    ADD CONSTRAINT job_queue_status_check
        CHECK (status IN ('QUEUED', 'RUNNING', 'FAILED'));
ALTER TABLE job_queue RENAME COLUMN retry_count TO attempts;

DROP INDEX idx_job_queue_status_created;
CREATE INDEX idx_job_queue_status_run_after ON job_queue(status, run_after);
