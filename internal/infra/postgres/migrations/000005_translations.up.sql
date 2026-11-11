-- A Termbase belongs to one Book and one target language, and is shared by
-- every Translation of that Book into that language (ADR-0006).
CREATE TABLE termbases (
    id UUID PRIMARY KEY,
    book_id UUID NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    -- Target language as a BCP 47 tag.
    language TEXT NOT NULL,
    status VARCHAR(20) NOT NULL CHECK (status IN ('EXTRACTING', 'IN_REVIEW', 'LOCKED', 'FAILED')),
    failure_reason TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    UNIQUE (book_id, language)
);

CREATE TABLE terms (
    termbase_id UUID NOT NULL REFERENCES termbases(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    rendering TEXT NOT NULL,
    kind VARCHAR(10) NOT NULL CHECK (kind IN ('name', 'term')),
    -- How often the source expression occurs in the Book's text.
    occurrences INT NOT NULL,
    PRIMARY KEY (termbase_id, source)
);

CREATE TABLE translations (
    id UUID PRIMARY KEY,
    book_id UUID NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    termbase_id UUID NOT NULL REFERENCES termbases(id) ON DELETE CASCADE,
    -- Same as the Termbase's language; repeated here so a Translation reads
    -- whole from one row.
    language TEXT NOT NULL,
    level VARCHAR(10) NOT NULL CHECK (level IN ('formal', 'informal', 'children')),
    status VARCHAR(20) NOT NULL
        CHECK (status IN ('EXTRACTING', 'IN_REVIEW', 'TRANSLATING', 'COMPLETE', 'FAILED', 'CANCELLED')),
    failure_reason TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_translations_book_id ON translations(book_id);
CREATE INDEX idx_translations_termbase_id ON translations(termbase_id);
