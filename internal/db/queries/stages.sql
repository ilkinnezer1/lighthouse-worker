-- name: StartStage :one
UPDATE deployment_stages
SET status     = 'RUNNING',
    started_at = now(),
    attempt    = attempt + 1
WHERE deployment_id = $1
  AND name = $2
RETURNING attempt, max_attempts;

-- name: FinishStage :exec
UPDATE deployment_stages
SET status      = $3,
    finished_at = now(),
    error_json  = $4
WHERE deployment_id = $1
  AND name = $2;