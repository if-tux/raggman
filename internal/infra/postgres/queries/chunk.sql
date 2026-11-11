-- name: CreateChunk :one
INSERT INTO chunks (
    id, document_id, page_number, content, embedding
) VALUES (
    $1, $2, $3, $4, $5
) RETURNING *;

-- name: SearchCorpus :many
-- Nearest Chunks by cosine similarity, from Ready Documents only (the Corpus).
-- The status filter applies after the HNSW scan; connections enable
-- hnsw.iterative_scan so the scan keeps going until top_k rows pass it. That
-- mode may return rows slightly out of order, hence the outer ORDER BY.
WITH nearest AS MATERIALIZED (
    SELECT
        c.id,
        c.document_id,
        c.page_number,
        c.content,
        d.filename,
        c.embedding <=> sqlc.arg(embedding) AS distance
    FROM chunks c
    JOIN documents d ON d.id = c.document_id
    WHERE d.status = 'READY'
    ORDER BY c.embedding <=> sqlc.arg(embedding)
    LIMIT sqlc.arg(top_k)
)
SELECT id, document_id, page_number, content, filename, (1 - distance)::float8 AS similarity
FROM nearest
ORDER BY distance;

-- name: DeleteChunksByDocument :exec
DELETE FROM chunks
WHERE document_id = $1;
