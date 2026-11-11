# Local by default; cloud only for Answers

Raggman runs entirely on local models out of the box: Ollama serves both embeddings and Answer generation. A user with an API key may point Answer generation at any OpenAI-compatible provider through configuration, but embeddings always stay local, so the Corpus never depends on a paid provider and never needs re-ingesting because a key changed or expired.

## Consequences

- There is one chat adapter speaking the OpenAI-compatible API; Ollama's own `/v1` endpoint is the default target. A provider-native adapter is added only when a needed feature is missing from the compatible API.
- Document text leaves the machine only when the user configures a cloud provider, and then only as the Chunks sent with a Question.
