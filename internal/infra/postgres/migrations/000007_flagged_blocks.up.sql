-- A Block whose translation still failed a check after every retry is Flagged:
-- its best attempt is kept, with the reason (ADR-0007). Warnings are doubts
-- that never reject a translation.
ALTER TABLE translated_blocks
    ADD COLUMN flag_reason TEXT,
    ADD COLUMN warnings TEXT[] NOT NULL DEFAULT '{}';
