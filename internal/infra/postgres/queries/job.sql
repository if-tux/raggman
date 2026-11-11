-- name: CreateJob :one
INSERT INTO job_queue (
    id, target_id, job_type, payload
) VALUES (
    $1, $2, $3, $4
) RETURNING *;

-- name: DequeueJob :one
-- Claims the next Job of one of the given types; other types stay queued.
UPDATE job_queue
SET status = 'RUNNING',
    locked_at = NOW(),
    locked_by = sqlc.arg(locked_by),
    heartbeat_at = NOW()
WHERE id = (
    SELECT id FROM job_queue
    WHERE job_type = ANY(sqlc.arg(job_types)::text[])
        AND ((status = 'QUEUED' AND run_after <= NOW())
            OR (status = 'RUNNING' AND heartbeat_at < NOW() - INTERVAL '5 minutes'))
    ORDER BY run_after ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1     
)
RETURNING *;

-- name: UpdateJobHeartBeat :exec
UPDATE job_queue
SET heartbeat_at = NOW()
WHERE id = $1 AND locked_by = $2;

-- name: CompleteJob :execrows
-- Only the worker holding the lock may complete a Job; 0 rows means the
-- Job was reclaimed by another worker. Also used to drop aborted Jobs.
DELETE FROM job_queue
WHERE id = $1 AND locked_by = $2;

-- The queries below act only for the worker holding the Job's lock; 0 rows
-- means the Job was reclaimed by another worker.

-- name: RetryJob :execrows
-- Counts a failed attempt and re-queues the Job after a backoff.
UPDATE job_queue
SET status = 'QUEUED',
    attempts = attempts + 1,
    run_after = NOW() + make_interval(secs => sqlc.arg(backoff_seconds)::float8),
    locked_at = NULL,
    locked_by = NULL,
    heartbeat_at = NULL
WHERE id = sqlc.arg(id) AND locked_by = sqlc.arg(locked_by);

-- name: FailJob :execrows
-- Counts the last failed attempt; the Job is not claimed again.
UPDATE job_queue
SET status = 'FAILED',
    attempts = attempts + 1,
    locked_at = NULL,
    locked_by = NULL,
    heartbeat_at = NULL
WHERE id = sqlc.arg(id) AND locked_by = sqlc.arg(locked_by);

-- name: ReleaseJob :execrows
-- Hands an unfinished Job back to the queue without counting an attempt.
UPDATE job_queue
SET status = 'QUEUED',
    run_after = NOW(),
    locked_at = NULL,
    locked_by = NULL,
    heartbeat_at = NULL
WHERE id = sqlc.arg(id) AND locked_by = sqlc.arg(locked_by);