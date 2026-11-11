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
