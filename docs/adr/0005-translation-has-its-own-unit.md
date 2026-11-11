# Translation uses Blocks and Windows, not Chunks

A Book is split into Blocks (whole paragraphs, headings, list items) and translated in Windows of consecutive Blocks that never cross a chapter. Each Block is translated exactly once. Chunks are the wrong unit: they overlap, so text would be translated twice, and they cut mid-sentence at a fixed size, which is harmless for retrieval and fatal for translation. Books therefore stay out of the Corpus, and ADR-0002 keeps applying to retrieval only.

A Block is never cut in what Raggman stores or exports. A Block too long for one model call (a paragraph of thousands of words) is sent to the model in parts cut between sentences, and the translated parts are joined back into the one Block before it is stored.

The model receives the Blocks of a Window numbered and must return the same numbers one to one, so a dropped or merged paragraph is a structural error rather than something to detect by reading the text.

## Considered Options

- **Reuse Chunks**: rejected for the overlap and mid-sentence cuts above.
- **One Block per call**: simplest alignment, but the model loses the neighbouring sentences it needs for pronouns and tone, and a novel becomes tens of thousands of calls. It remains the fallback when a Window keeps failing.
- **Whole chapter per call**: rejected. It exceeds a local model's context, and long outputs are where omissions happen.
