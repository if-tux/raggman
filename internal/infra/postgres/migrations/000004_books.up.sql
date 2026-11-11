-- Books are translated, never searched: they have their own tables and stay
-- out of the Corpus (ADR-0005).
CREATE TABLE books (
    id UUID PRIMARY KEY,
    file_hash VARCHAR(64) UNIQUE NOT NULL,
    filename TEXT NOT NULL,
    title TEXT NOT NULL,
    -- Source language as the EPUB declares it; empty when it declares none.
    language TEXT NOT NULL,
    total_blocks INT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- Original EPUB bytes, kept to rebuild the translated EPUB from. Separate from
-- books so listing stays light.
CREATE TABLE book_files (
    book_id UUID PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
    content BYTEA NOT NULL
);

CREATE TABLE blocks (
    book_id UUID NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    -- Reading order within the Book, from 0.
    position INT NOT NULL,
    -- Position of the Block's chapter in the reading order, from 1; 0 for the
    -- title and the table of contents.
    chapter INT NOT NULL,
    kind VARCHAR(10) NOT NULL CHECK (kind IN ('title', 'toc', 'text', 'alt')),
    source TEXT NOT NULL,
    PRIMARY KEY (book_id, position)
);
