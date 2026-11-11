-- name: CreateChunk :one
INSERT INTO chunks (
    id, document_id, page_number, content, embedding
) VALUES (
    $1, $2, $3, $4, $5
) RETURNING *;

-- name: SearchChunks :many
SELECT 
    id, 
    document_id,
    page_number,
    content,
    (1 -(embedding <=> $1))::float8 AS similarity_score
FROM chunks
ORDER By embedding <=> $1
LIMIT $2;
-- name: DeleteChunksByDocument :exec
DELETE FROM chunks
WHERE document_id = $1;
