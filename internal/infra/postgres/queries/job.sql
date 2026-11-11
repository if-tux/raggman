-- name: CreateJob :one
INSERT INTO job_queue (
    id, target_id, job_type, payload
) VALUES (
    $1, $2, $3, $4
) RETURNING *;

-- name: DequeueJob :one
UPDATE job_queue
SET status = 'RUNNING',
    locked_at = NOW(),
    locked_by = $1,
    heartbeat_at = NOW()
WHERE id = (
    SELECT id FROM job_queue
    WHERE (status = 'QUEUED' AND run_after <= NOW())
        OR (status = 'RUNNING' AND heartbeat_at < NOW() - INTERVAL '5 minutes')
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
-- Job was reclaimed by another worker.
DELETE FROM job_queue
WHERE id = $1 AND locked_by = $2;

-- name: FailedJob :exec
UPDATE job_queue
SET status = 'FAILED',
    attempts = attempts + 1
WHERE id = $1;