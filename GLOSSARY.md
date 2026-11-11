# Raggman

Raggman answers natural-language Questions with Answers grounded in a shared Corpus of PDF Documents, citing the Chunks it drew from. It also translates Books into another language at a chosen Level.

## Corpus

**Corpus**:
The single shared set of Ready Documents that every Question is answered against.
_Avoid_: knowledge base, collection, index

**Document**:
A PDF submitted to Raggman, identified by its content: identical bytes are the same Document regardless of filename.
_Avoid_: file, upload, attachment

**Ready**:
The state of a Document whose Ingestion has fully succeeded; only Ready Documents belong to the Corpus.
_Avoid_: done, indexed, complete

**Failed**:
The state of a Document whose Ingestion exhausted its attempts; it stays out of the Corpus until it is submitted again.
_Avoid_: error, broken

**Page**:
One page of a Document; the finest location an Answer can point to.

**Chunk**:
A passage of text from a single Page that retrieval ranks and Citations point to; a Chunk never spans Pages.
_Avoid_: segment, fragment, snippet

**Ingestion**:
The process that turns a Document into searchable Chunks.
_Avoid_: indexing, processing, import

## Answering

**Question**:
A natural-language request for information from the Corpus.
_Avoid_: query, prompt, search

**Answer**:
A generated response to a single Question, drawn only from Chunks in the Corpus; an Answer without Citations means the Corpus holds nothing relevant.
_Avoid_: response, completion, reply

**Citation**:
A reference from an Answer to a Chunk that supports it, locating that Chunk's Document and Page.
_Avoid_: source, reference

## Translation

**Book**:
An EPUB submitted to Raggman for translation, identified by its content like a Document; a Book never belongs to the Corpus.
_Avoid_: document, file, ebook

**Translation**:
One Book rendered into one target language at one Level.
_Avoid_: job, run, version

**Level**:
The register a Translation is written in: formal, informal or children's. A Level changes wording only, never content.
_Avoid_: style, tone, mode

**Block**:
One piece of a Book's text that stands on its own: a paragraph, heading, list item or table cell, or the Book's title, a table-of-contents entry or an image description. The atomic unit of translation, translated exactly once and never cut.
_Avoid_: segment, chunk, paragraph, sentence

**Window**:
A run of consecutive Blocks from one chapter sent to the model in a single call; a Window never crosses a chapter.
_Avoid_: batch, chunk, context

**Termbase**:
The fixed list of Terms for one Book and one target language, reviewed by the user and locked before any Block is translated.
_Avoid_: glossary, dictionary, vocabulary

**Term**:
One entry of a Termbase: a source expression and its fixed rendering, either a name (must appear exactly) or a term (may inflect).
_Avoid_: entry, keyword, entity

**Flagged**:
The state of a Block whose translation still fails a check after every retry; its best attempt is kept and listed in the Translation's report.
_Avoid_: failed, broken, rejected
