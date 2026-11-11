# Chunks never span Pages

A Chunk always lies within a single Page, even when a paragraph continues across a page break. This keeps every Citation pointing at exactly one Page, at the cost of slightly worse retrieval for passages split by a page break. Changing this later means re-ingesting every Document and giving Citations a page range instead of a page.
