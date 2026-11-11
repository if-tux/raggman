// Package queue runs background Jobs from the Postgres job table (ADR-0003):
// workers claim Jobs, keep their locks alive, retry failures with backoff and
// hand unfinished Jobs back on shutdown. What a Job does is up to the Handler
// registered for its type.
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ponytail: fixed backoff after each failed attempt; make it configurable if
// real outages last longer. Its length is maxAttempts-1.
var backoffs = []time.Duration{10 * time.Second, 40 * time.Second}

const (
	pollInterval = time.Second
	// defaultShutdownGrace is how long an in-flight Job may keep running after
	// shutdown starts. Grace plus settleTimeout stays under the app's 30s
	// shutdown timeout, so a Job is released before the pool closes.
	defaultShutdownGrace = 20 * time.Second
	settleTimeout        = 5 * time.Second
	// maxAttempts is how many times a Job is tried before it fails for good.
	maxAttempts       = 3
	heartbeatInterval = 30 * time.Second
)

// Handler does the work of one type of Job.
type Handler interface {
	// Name is what logs and failure reasons call the work, e.g. "Ingestion".
	Name() string
	// TargetKey is the log attribute that carries the Job's target id, e.g.
	// "document_id", so logs can be searched by what the Job worked on.
	TargetKey() string
	// Work runs the Job. It must end by calling Complete inside the
	// transaction that records its result, so a worker that lost its lock
	// writes nothing. An error wrapped by Permanent fails the Job at once;
	// any other error is retried.
	Work(ctx context.Context, job sqlc.JobQueue) error
	// TargetGone reports whether what the Job works on was removed, in which
	// case a failed Job is dropped instead of retried.
	TargetGone(ctx context.Context, job sqlc.JobQueue) (bool, error)
	// MarkFailed records, through q, that the work failed for good, with a
	// reason the user can read. q is the transaction that also ends the Job.
	MarkFailed(ctx context.Context, q *sqlc.Queries, job sqlc.JobQueue, reason string) error
}

// Handlers maps each Job type to its Handler.
type Handlers map[string]Handler

// Group is a set of workers that claim only the Job types of its Handlers.
// Separate Groups keep slow Jobs from holding up quick ones: a Termbase
// extraction runs for hours and must not keep Documents waiting.
type Group struct {
	Handlers Handlers
	// Workers is how many Jobs of this Group run at once.
	// ponytail: fixed per Group; local inference is the bottleneck, so more
	// workers only queue up at Ollama. Make it configurable for cloud setups.
	Workers int
}

// Permanent marks err as one that retrying cannot fix. Its message becomes
// the failure reason as it is.
func Permanent(err error) error { return permanentError{err} }

type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

// ErrLostLock means another worker reclaimed the Job; this attempt must stop
// without writing anything.
var ErrLostLock = errors.New("job was reclaimed by another worker")

// Complete removes the Job, only if this worker still holds its lock.
func Complete(ctx context.Context, q *sqlc.Queries, job sqlc.JobQueue) error {
	n, err := q.CompleteJob(ctx, sqlc.CompleteJobParams{ID: job.ID, LockedBy: job.LockedBy})
	if err != nil {
		return fmt.Errorf("completing job: %w", err)
	}
	if n == 0 {
		return ErrLostLock
	}
	return nil
}

// Workers run Jobs. ShutdownGrace is how long an in-flight Job may finish
// after shutdown starts; tests lower it.
type Workers struct {
	tracer        trace.Tracer
	pool          *pgxpool.Pool
	queries       *sqlc.Queries
	logger        *slog.Logger
	groups        []Group
	ShutdownGrace time.Duration
}

func New(pool *pgxpool.Pool, logger *slog.Logger, tp *sdktrace.TracerProvider, groups []Group) *Workers {
	return &Workers{
		tracer:  tp.Tracer("raggman/queue"),
		pool:    pool,
		queries: sqlc.New(pool),
		logger:  logger,
		groups:  groups,

		ShutdownGrace: defaultShutdownGrace,
	}
}

// Run works through Jobs until ctx is cancelled. Workers then stop claiming
// Jobs, give an in-flight Job up to ShutdownGrace to finish (then hand it
// back to the queue), and Run returns once every worker has stopped.
func (w *Workers) Run(ctx context.Context) {
	host, _ := os.Hostname()
	var wg sync.WaitGroup
	for g, group := range w.groups {
		for i := range group.Workers {
			id := fmt.Sprintf("%s-%d-%d", host, g, i)
			wg.Go(func() { w.loop(ctx, id, group.Handlers) })
		}
	}
	wg.Wait()
}

func (w *Workers) loop(ctx context.Context, workerID string, handlers Handlers) {
	// Only Jobs with a Handler are claimed; others wait in the queue.
	types := slices.Sorted(maps.Keys(handlers))
	for {
		job, err := w.queries.DequeueJob(ctx, sqlc.DequeueJobParams{
			LockedBy: pgtype.Text{String: workerID, Valid: true},
			JobTypes: types,
		})
		if err == nil {
			w.runJob(ctx, workerID, handlers[job.JobType], job)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			w.logger.ErrorContext(ctx, "Claiming Job failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

func (w *Workers) runJob(ctx context.Context, workerID string, handler Handler, job sqlc.JobQueue) {
	name := handler.Name()
	log := w.logger.With("job_id", job.ID, "job_type", job.JobType, handler.TargetKey(), job.TargetID, "worker", workerID)

	// The Job continues the trace of the request that enqueued it; each
	// attempt is its own span in that trace.
	spanCtx, span := w.tracer.Start(telemetry.FromCarrier(context.WithoutCancel(ctx), job.Payload), strings.ToLower(job.JobType),
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			// "document_id" in logs is "document.id" on spans.
			attribute.String(strings.ReplaceAll(handler.TargetKey(), "_", "."), job.TargetID.String()),
			attribute.Int("job.attempt", int(job.Attempts)+1),
		))
	defer span.End()
	log.InfoContext(spanCtx, name+" started")

	// The Job outlives ctx by ShutdownGrace, so shutdown does not abort it at once.
	jobCtx, cancel := context.WithCancel(spanCtx)
	defer cancel()
	grace := w.ShutdownGrace
	stopGrace := context.AfterFunc(ctx, func() { time.AfterFunc(grace, cancel) })
	defer stopGrace()

	stopHeartbeat := w.heartbeat(jobCtx, job.ID, workerID)
	err := handler.Work(jobCtx, job)
	stopHeartbeat()
	telemetry.RecordError(jobCtx, span, err)

	// Settle the Job even when jobCtx is cancelled, but never hang on it.
	settleCtx, cancelSettle := context.WithTimeout(spanCtx, settleTimeout)
	defer cancelSettle()

	switch {
	case err == nil:
		log.InfoContext(spanCtx, name+" finished")
	case errors.Is(err, ErrLostLock):
		log.WarnContext(spanCtx, "Job was reclaimed by another worker; dropping this attempt")
	case jobCtx.Err() != nil:
		log.InfoContext(spanCtx, "Shutdown grace ran out; releasing Job")
		w.settle(settleCtx, log, "releasing", func() (int64, error) {
			return w.queries.ReleaseJob(settleCtx, sqlc.ReleaseJobParams{ID: job.ID, LockedBy: job.LockedBy})
		})
	default:
		w.fail(settleCtx, log, handler, job, err)
	}
}

// fail decides what a failed attempt means: drop the Job if its target is
// gone, retry with backoff, or fail for good after the last attempt.
func (w *Workers) fail(ctx context.Context, log *slog.Logger, handler Handler, job sqlc.JobQueue, cause error) {
	name := handler.Name()
	gone, err := handler.TargetGone(ctx, job)
	if err != nil {
		log.ErrorContext(ctx, "Loading the Job's target after a failed attempt", "error", err)
		return // the lock goes stale and the Job is reclaimed later
	}
	if gone {
		log.InfoContext(ctx, "Target was removed; aborting "+name)
		w.settle(ctx, log, "aborting", func() (int64, error) {
			return w.queries.CompleteJob(ctx, sqlc.CompleteJobParams{ID: job.ID, LockedBy: job.LockedBy})
		})
		return
	}

	// ponytail: an attempt counts only when it fails cleanly; a crash mid-Job is
	// reclaimed without counting, so input that crashes the process retries
	// forever. Count reclaims too if that ever happens.
	attempt := job.Attempts + 1
	var reason string
	var permanent permanentError
	switch {
	case errors.As(cause, &permanent):
		reason = permanent.Error()
	case attempt < maxAttempts:
		backoff := backoffs[attempt-1]
		log.WarnContext(ctx, name+" attempt failed; retrying", "attempt", attempt, "backoff", backoff.String(), "error", cause)
		w.settle(ctx, log, "re-queuing", func() (int64, error) {
			return w.queries.RetryJob(ctx, sqlc.RetryJobParams{ID: job.ID, LockedBy: job.LockedBy, BackoffSeconds: backoff.Seconds()})
		})
		return
	default:
		reason = fmt.Sprintf("%s failed after %d attempts: %v", name, attempt, cause)
	}
	log.ErrorContext(ctx, name+" failed for good", "attempt", attempt, "reason", reason)
	if err := w.markFailed(ctx, handler, job, reason); err != nil {
		log.ErrorContext(ctx, "Recording the failure", "error", err)
	}
}

// markFailed ends the Job and records the failure on its target in one
// transaction. The Job row stays as FAILED for inspection.
// ponytail: FAILED Job rows are never cleaned up; purge old ones if the
// table grows.
func (w *Workers) markFailed(ctx context.Context, handler Handler, job sqlc.JobQueue, reason string) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := w.queries.WithTx(tx)

	n, err := q.FailJob(ctx, sqlc.FailJobParams{ID: job.ID, LockedBy: job.LockedBy})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLostLock
	}
	if err := handler.MarkFailed(ctx, q, job, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// settle runs a lock-guarded Job update and logs when it did not apply.
func (w *Workers) settle(ctx context.Context, log *slog.Logger, action string, update func() (int64, error)) {
	n, err := update()
	switch {
	case err != nil:
		log.ErrorContext(ctx, "Settling Job failed", "action", action, "error", err)
	case n == 0:
		log.WarnContext(ctx, "Job was reclaimed by another worker", "action", action)
	}
}

// heartbeat keeps the Job's lock fresh so it is not reclaimed mid-work.
func (w *Workers) heartbeat(ctx context.Context, jobID uuid.UUID, workerID string) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := w.queries.UpdateJobHeartBeat(ctx, sqlc.UpdateJobHeartBeatParams{
					ID: jobID, LockedBy: pgtype.Text{String: workerID, Valid: true},
				})
				if err != nil && ctx.Err() == nil {
					w.logger.WarnContext(ctx, "Job heartbeat failed", "job_id", jobID, "error", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
