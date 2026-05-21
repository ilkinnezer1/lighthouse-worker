-- name: InsertService :exec
INSERT INTO services (deployment_id, name, image, container_id, host, port, is_web, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListServicesByEnvironment :many
SELECT s.id,
       s.deployment_id,
       s.name,
       s.image,
       s.container_id,
       s.host,
       s.port,
       s.is_web,
       s.status
FROM services s
         JOIN deployments d ON d.id = s.deployment_id
WHERE d.environment_id = $1;

-- name: MarkServiceTornDown :exec
UPDATE services
SET status       = 'torn_down',
    container_id = NULL
WHERE id = $1;

-- name: DeleteServicesByDeployment :exec
DELETE FROM services
WHERE deployment_id = $1;
