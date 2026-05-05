-- name: InsertService :exec
INSERT INTO services (deployment_id, name, image, container_id, host, port, is_web, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);
