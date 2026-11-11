-- name: CreateBook :one
-- Returns no row when a Book with the same content already exists.
INSERT INTO books (
    id, file_hash, filename, title, language, total_blocks
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (file_hash) DO NOTHING
RETURNING *;

-- name: CreateBookFile :exec
INSERT INTO book_files (book_id, content)
VALUES ($1, $2);

-- name: CreateBlocks :copyfrom
INSERT INTO blocks (book_id, position, chapter, kind, source)
VALUES ($1, $2, $3, $4, $5);

-- name: GetBookByHash :one
SELECT * FROM books
WHERE file_hash = $1;

-- name: GetBook :one
SELECT * FROM books
WHERE id = $1;

-- name: GetBookForShare :one
-- Keeps the Book from being removed until the transaction ends.
SELECT * FROM books
WHERE id = $1
FOR SHARE;

-- name: ListBooks :many
SELECT * FROM books
ORDER BY created_at DESC;

-- name: DeleteBook :execrows
DELETE FROM books
WHERE id = $1;
