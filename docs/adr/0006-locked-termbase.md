# A reviewed, locked Termbase is the only source of names

Before any Block is translated, a pass over the whole Book extracts candidate Terms, the user reviews them, and the Termbase is locked. Every Window is sent with the Terms that occur in it. The model has no memory across a Book, so without a fixed list a character can be "John" in chapter 1 and "João" in chapter 20; the Termbase remembers on the model's behalf.

Names of people default to their source spelling. A Termbase belongs to a Book and a target language and is shared by every Level. It cannot change while a Translation runs: changing it means a new Translation.

## Consequences

- A Translation pauses for review before it starts. This is deliberate: a wrong Term is repeated across the whole Book, so minutes of review save hours of retranslation.
- Terms of type name are enforced exactly. Terms of type term are only suggested to the model, because target languages inflect them ("elfo", "elfos") and an exact check would reject correct translations.

## Considered Options

- **Rolling context only** (the previous translated Blocks): rejected as the sole defence. It keeps names stable across neighbouring Windows and drifts over hundreds of pages.
- **Retrieve earlier translations by similarity**: rejected for now. It needs embeddings for every Block and still returns whatever the model wrote earlier, including its mistakes.
