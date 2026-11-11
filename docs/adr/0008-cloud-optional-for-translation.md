# Cloud is optional for Translation too

Extends ADR-0004. Translation and Termbase extraction use the same chat adapter and configuration as Answers: local Ollama by default, any OpenAI-compatible provider when the user sets a key. Embeddings are not involved.

The exposure is larger than for Answers: a Question sends a few Chunks, a Translation sends the entire Book. So when a cloud provider is configured, the UI says so before a Translation starts, and the API reports whether the chat target is local.
