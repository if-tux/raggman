-- name: CreateTermbase :one
-- Returns no row when the Book already has a Termbase for the language.
INSERT INTO termbases (id, book_id, language, status)
VALUES ($1, $2, $3, $4)
ON CONFLICT (book_id, language) DO NOTHING
RETURNING *;

-- name: GetTermbase :one
SELECT * FROM termbases
WHERE id = $1;

-- name: GetTermbaseForUpdate :one
SELECT * FROM termbases
WHERE id = $1
FOR UPDATE;

-- name: GetTermbaseByLanguageForUpdate :one
SELECT * FROM termbases
WHERE book_id = $1 AND language = $2
FOR UPDATE;

-- name: UpdateTermbaseStatus :exec
UPDATE termbases
SET status = $2,
    failure_reason = $3
WHERE id = $1;

-- name: DeleteTerms :exec
DELETE FROM terms
WHERE termbase_id = $1;

-- name: CreateTerms :copyfrom
INSERT INTO terms (termbase_id, source, rendering, kind, occurrences)
VALUES ($1, $2, $3, $4, $5);

-- name: ListTerms :many
-- Most frequent first: that is where a reviewer's attention pays off.
SELECT * FROM terms
WHERE termbase_id = $1
ORDER BY occurrences DESC, source;

-- name: CreateTranslation :one
INSERT INTO translations (id, book_id, termbase_id, language, level, status)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetTranslation :one
SELECT * FROM translations
WHERE id = $1;

-- name: ListTranslations :many
SELECT * FROM translations
ORDER BY created_at, id;

-- name: ListTranslationsByBook :many
SELECT * FROM translations
WHERE book_id = $1
ORDER BY created_at, id;

-- name: MoveTranslations :many
-- Moves every Translation of a Termbase from one status to another.
UPDATE translations
SET status = sqlc.arg(to_status),
    failure_reason = sqlc.narg(failure_reason)
WHERE termbase_id = sqlc.arg(termbase_id) AND status = sqlc.arg(from_status)
RETURNING id;

-- name: ListTextBlocks :many
SELECT chapter, source FROM blocks
WHERE book_id = $1 AND kind = 'text'
ORDER BY position;

-- name: ListBlocks :many
SELECT position, chapter, kind, source FROM blocks
WHERE book_id = $1
ORDER BY position;

-- name: ListTranslatedBlocks :many
SELECT position, translation, (flag_reason IS NOT NULL)::bool AS flagged FROM translated_blocks
WHERE translation_id = $1
ORDER BY position;

-- name: CreateTranslatedBlock :exec
-- A Block already translated by an earlier attempt keeps its translation.
INSERT INTO translated_blocks (translation_id, position, translation, flag_reason, warnings)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (translation_id, position) DO NOTHING;

-- name: ListReportedBlocks :many
-- The Blocks of a Translation that are Flagged or carry warnings, in reading order.
SELECT tb.position, b.chapter, b.source, tb.translation, tb.flag_reason, tb.warnings
FROM translated_blocks tb
JOIN translations t ON t.id = tb.translation_id
JOIN blocks b ON b.book_id = t.book_id AND b.position = tb.position
WHERE tb.translation_id = $1
    AND (tb.flag_reason IS NOT NULL OR cardinality(tb.warnings) > 0)
ORDER BY tb.position;

-- name: GetTranslationProgress :one
SELECT
    (SELECT count(*) FROM translated_blocks tb WHERE tb.translation_id = t.id)::int AS translated,
    (SELECT count(*) FROM translated_blocks tb WHERE tb.translation_id = t.id AND tb.flag_reason IS NOT NULL)::int AS flagged,
    b.total_blocks AS total
FROM translations t
JOIN books b ON b.id = t.book_id
WHERE t.id = $1;

-- name: UpdateTranslationStatus :execrows
-- Moves one Translation from one status to another; 0 rows means it was no
-- longer in the expected status.
UPDATE translations
SET status = sqlc.arg(to_status),
    failure_reason = sqlc.narg(failure_reason)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status);
