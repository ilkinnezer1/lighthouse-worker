-- name: StartStage :exec
UPDATE deployment_stages
SET status     = 'RUNNING',
    started_at = now()
WHERE deployment_id = $1
  AND name = $2;

-- name: FinishStage :exec
UPDATE deployment_stages
SET status      = $3,
    finished_at = now(),
    error_json  = $4
WHERE deployment_id = $1
  AND name = $2;