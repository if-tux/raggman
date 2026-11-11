-- One row per Block a Translation has finished, written Window by Window, so
-- an interrupted Translation resumes where it stopped.
CREATE TABLE translated_blocks (
    translation_id UUID NOT NULL REFERENCES translations(id) ON DELETE CASCADE,
    -- The Block's position in its Book.
    position INT NOT NULL,
    translation TEXT NOT NULL,
    PRIMARY KEY (translation_id, position)
);
