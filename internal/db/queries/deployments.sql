-- name: GetDeploymentForJob :one
SELECT d.id,
       d.environment_id,
       d.triggered_by_user_id,
       d.status,
       d.cancel_requested_at,
       e.name              AS env_name,
       e.branch,
       e.subdomain         AS env_subdomain,
       e.custom_domain,
       e.env_vars_enc,
       p.id                AS project_id,
       p.subdomain         AS project_subdomain,
       p.repo_url,
       p.default_branch,
       p.build_config,
       p.owner_id,
       u.username::text    AS owner_username
FROM deployments d
         JOIN environments e ON e.id = d.environment_id
         JOIN projects p ON p.id = e.project_id
         JOIN users u ON u.id = p.owner_id
WHERE d.id = $1;

-- name: GetDeploymentBranches :many
SELECT role, name, sha, ordinal
FROM deployment_branches
WHERE deployment_id = $1
ORDER BY ordinal;

-- name: GetCancelRequestedAt :one
SELECT cancel_requested_at
FROM deployments
WHERE id = $1;

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

-- name: GetEnvironmentForTeardown :one
SELECT e.id,
       e.name           AS env_name,
       e.subdomain      AS env_subdomain,
       e.active,
       p.id             AS project_id,
       p.owner_id,
       u.username::text AS owner_username
FROM environments e
         JOIN projects p ON p.id = e.project_id
         JOIN users u ON u.id = p.owner_id
WHERE e.id = $1;

-- name: ListDeploymentIDsByEnvironment :many
SELECT id, status
FROM deployments
WHERE environment_id = $1;

-- name: DeactivateEnvironment :exec
UPDATE environments
SET active     = false,
    updated_at = now()
WHERE id = $1;

-- name: UpdateDeploymentCommit :exec
UPDATE deployments
SET commit_sha     = $2,
    commit_message = $3,
    commit_author  = $4
WHERE id = $1;

-- name: UpdateDeploymentFramework :exec
UPDATE deployments
SET framework = $2
WHERE id = $1;

-- name: ListManagedFilesByEnvironment :many
SELECT path, content_enc
FROM managed_files
WHERE environment_id = $1
ORDER BY path;