-- name: CreateDocument :one
-- Returns no row when a Document with the same content already exists.
INSERT INTO documents (
    id, file_hash, filename, status
) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (file_hash) DO NOTHING
RETURNING *;

-- name: CreateDocumentFile :exec
INSERT INTO document_files (document_id, content)
VALUES ($1, $2);

-- name: GetDocumentByHashForUpdate :one
SELECT * FROM documents
WHERE file_hash = $1
FOR UPDATE;

-- name: GetDocument :one
SELECT * FROM documents
WHERE id = $1;

-- name: ListDocuments :many
SELECT * FROM documents
ORDER BY created_at DESC;

-- name: UpdateDocumentStatus :exec
UPDATE documents
SET status = $2,
    failure_reason = $3
WHERE id = $1;

-- name: DeleteDocument :execrows
DELETE FROM documents
WHERE id = $1;
