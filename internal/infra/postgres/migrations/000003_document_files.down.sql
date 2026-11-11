DROP TABLE document_files;
UPDATE documents SET total_pages = 0 WHERE total_pages IS NULL;
ALTER TABLE documents ALTER COLUMN total_pages SET NOT NULL;
