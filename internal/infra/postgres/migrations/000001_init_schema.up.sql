CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE documents (
    id UUID PRIMARY KEY,
    file_hash VARCHAR(64) UNIQUE NOT NULL,
    filename TEXT NOT NULL,
    total_pages INT NOT NULL,
    status VARCHAR(50) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE chunks (
    id UUID PRIMARY KEY,
    document_id UUID REFERENCES documents(id) ON DELETE CASCADE,
    page_number INT NOT NULL,
    content TEXT NOT NULL,
    embedding vector(3584)
);

CREATE TABLE job_queue (
    id UUID PRIMARY KEY,
    target_id UUID NOT NULL,
    job_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(20) DEFAULT 'QUEUED',
    locked_at TIMESTAMP WITH TIME ZONE,
    locked_by VARCHAR(255),
    heartbeat_at TIMESTAMP WITH TIME ZONE,
    retry_count INT DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_job_queue_status_created ON job_queue(status, created_at);
CREATE INDEX idx_chunks_document_id ON chunks(document_id);