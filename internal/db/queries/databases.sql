-- name: GetDatabase :one
SELECT id, environment_id, type, version, name, status, owns_container,
       host, port, conn_secret_enc
FROM managed_databases
WHERE id = $1;

-- name: ListReadyDatabasesByEnvironment :many
SELECT id, name, host, port, conn_secret_enc
FROM managed_databases
WHERE environment_id = $1
  AND status = 'READY'
  AND host IS NOT NULL
  AND conn_secret_enc IS NOT NULL;

-- name: MarkDatabaseReady :exec
UPDATE managed_databases
SET status          = 'READY',
    host            = $2,
    port            = $3,
    conn_secret_enc = $4,
    updated_at      = now()
WHERE id = $1;

-- name: MarkDatabaseFailed :exec
UPDATE managed_databases
SET status        = 'FAILED',
    error_summary = $2,
    updated_at    = now()
WHERE id = $1;

-- name: GetSnapshot :one
SELECT id, database_id, status, storage_key, size_bytes
FROM database_snapshots
WHERE id = $1;

-- name: MarkSnapshotReady :exec
UPDATE database_snapshots
SET status      = 'READY',
    storage_key = $2,
    size_bytes  = $3
WHERE id = $1;

-- name: MarkSnapshotFailed :exec
UPDATE database_snapshots
SET status        = 'FAILED',
    error_summary = $2
WHERE id = $1;
