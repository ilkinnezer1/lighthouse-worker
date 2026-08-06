-- name: GetVolumeBackup :one
SELECT *
FROM volume_backups
WHERE id = $1;

-- name: MarkVolumeBackupReady :exec
UPDATE volume_backups
SET status       = 'READY',
    storage_key  = $2,
    size_bytes   = $3,
    completed_at = now()
WHERE id = $1;

-- name: MarkVolumeBackupFailed :exec
UPDATE volume_backups
SET status        = 'FAILED',
    error_summary = $2,
    completed_at  = now()
WHERE id = $1;
