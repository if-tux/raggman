-- The page count is only known once Ingestion has read the PDF.
ALTER TABLE documents ALTER COLUMN total_pages DROP NOT NULL;

-- Original PDF bytes, kept so Ingestion can run (and re-run after an embedding
-- model change, ADR-0001). Separate from documents so listing stays light.
CREATE TABLE document_files (
    document_id UUID PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    content BYTEA NOT NULL
);
