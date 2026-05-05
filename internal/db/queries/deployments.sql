-- name: GetDeploymentForJob :one
SELECT d.id,
       d.environment_id,
       d.triggered_by_user_id,
       d.status,
       e.branch,
       e.subdomain,
       e.env_vars_enc,
       p.id AS project_id,
       p.repo_url,
       p.default_branch,
       p.build_config,
       p.owner_id
FROM deployments d
         JOIN environments e ON e.id = d.environment_id
         JOIN projects p ON p.id = e.project_id
WHERE d.id = $1;

-- name: StartDeployment :exec
UPDATE deployments
SET status     = 'CLONING',
    started_at = now()
WHERE id = $1;

-- name: TransitionDeploymentStatus :exec
UPDATE deployments
SET status = $2
WHERE id = $1;

-- name: FinishDeployment :exec
UPDATE deployments
SET status        = $2,
    finished_at   = now(),
    error_summary = $3
WHERE id = $1;

-- name: UpdateHeartbeat :exec
UPDATE deployments
SET last_heartbeat_at = now()
WHERE id = $1;