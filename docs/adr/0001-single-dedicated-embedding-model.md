# One dedicated embedding model for the whole Corpus

Every Chunk in the Corpus is embedded with a single dedicated embedding model, `qwen3-embedding:0.6b` (1024 dimensions, multilingual, served by Ollama). Similarity scores are only comparable within one model, so mixing models would break retrieval; switching models therefore means re-ingesting every Document.

## Considered Options

- **Chat model hidden states (3584 dims, Qwen2/2.5-7B)**: the original schema's choice. Rejected: not trained for retrieval, and above pgvector's 2000-dimension index limit for `vector`, forcing a full scan on every Question.
- **`nomic-embed-text` (768 dims)**: smaller and fast, but English-focused; rejected because the Corpus is expected to be multilingual.
