-- name: CreateDocument :one
INSERT INTO documents (
    id, file_hash, filename, total_pages, status
) VALUES (
    $1, $2, $3, $4, $5
) RETURNING *;

-- name: GetDocumentByHash :one
SELECT * FROM documents
WHERE file_hash = $1 LIMIT 1;

-- name: UpdateDocumentStatus :exec
UPDATE documents
SET status = $2
WHERE id = $1;