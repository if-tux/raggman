# Deterministic checks first; no LLM judge yet

Every translated Window is verified without a model: the Block count and numbering match, names from the Termbase appear exactly, inline formatting markers survive, the length ratio to the source is within a range, and no Block comes back identical to its source. A failing Window is retried with the error, then split into single Blocks, and a Block that still fails becomes Flagged while the Translation continues.

A second model call that compares source and translation would catch omissions and additions inside a Block, which these checks cannot. It is left out because it doubles the running time of a job that already takes hours locally, and a judge is itself a model that can be wrong. Add it only if measurements on a full novel show the deterministic checks miss too much.

## Consequences

- A Translation never fails because of one bad Block; it finishes with a report of Flagged Blocks.
- Thresholds (length ratio, Window size, context size, temperature, thinking) are set from measurement, not chosen up front.
