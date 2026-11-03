package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ClaimBulkJobs atomically claims due jobs for one worker.
func (s *Store) ClaimBulkJobs(ctx context.Context, limit int) ([]BulkJob, error) {
	if limit <= 0 {
		limit = 5
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT id, kind, batch_id, run_after, status, attempts, max_attempts, last_error
		FROM bulk_jobs
		WHERE status=$1 AND run_after <= now()
		ORDER BY run_after ASC
		FOR UPDATE SKIP LOCKED
		LIMIT $2`, BulkJobQueued, limit)
	if err != nil {
		return nil, err
	}
	var jobs []BulkJob
	for rows.Next() {
		var j BulkJob
		if err := rows.Scan(&j.ID, &j.Kind, &j.BatchID, &j.RunAfter, &j.Status, &j.Attempts, &j.MaxAttempts, &j.LastError); err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range jobs {
		if _, err := tx.Exec(ctx, `UPDATE bulk_jobs SET status=$1, attempts=attempts+1, updated_at=now() WHERE id=$2`, BulkJobRunning, jobs[i].ID); err != nil {
			return nil, err
		}
		jobs[i].Status = BulkJobRunning
		jobs[i].Attempts++
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return jobs, nil
}

// CompleteBulkJob marks a job done.
func (s *Store) CompleteBulkJob(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE bulk_jobs SET status=$1, last_error='', updated_at=now() WHERE id=$2`, BulkJobDone, id)
	return err
}

// RetryBulkJob requeues a job with an exponential backoff, or marks it failed
// once it exhausts max_attempts.
func (s *Store) RetryBulkJob(ctx context.Context, id uuid.UUID, jobErr string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var attempts, maxAttempts int
	if err := tx.QueryRow(ctx, `SELECT attempts, max_attempts FROM bulk_jobs WHERE id=$1 FOR UPDATE`, id).Scan(&attempts, &maxAttempts); err != nil {
		return err
	}
	jobErr = truncateBulkText(jobErr)
	if attempts >= maxAttempts {
		if _, err := tx.Exec(ctx, `UPDATE bulk_jobs SET status=$1, last_error=$2, updated_at=now() WHERE id=$3`, BulkJobFailed, jobErr, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	backoff := time.Duration(1<<uint(attempts)) * 15 * time.Second
	if _, err := tx.Exec(ctx, `
		UPDATE bulk_jobs SET status=$1, run_after=now()+$2::interval, last_error=$3, updated_at=now() WHERE id=$4`,
		BulkJobQueued, backoff.String(), jobErr, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
