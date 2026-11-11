# Raggman

Raggman answers natural-language Questions with Answers grounded in a shared Corpus of PDF Documents, citing the Chunks it drew from.

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
