# Postgres as the job queue

Background work (Ingestion) runs from a job table in Postgres, claimed with `FOR UPDATE SKIP LOCKED`, instead of a message broker. Submitting a Document and enqueuing its Ingestion happen in one transaction, so no Document can be left waiting on a message that was never published; and the throughput ceiling of a Postgres queue is far above what local LLM inference can consume.

## Considered Options

- **RabbitMQ**: rejected. Atomic enqueue would need an outbox table in Postgres anyway, it adds a service to the one-command `docker compose up` setup, and its strengths (fanout, routing, very high throughput) are not needs here. Revisit if several independent services must consume the same events.
