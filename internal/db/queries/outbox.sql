-- name: InsertOutboxEvent :exec
INSERT INTO event_outbox (type, user_id, payload)
VALUES ($1, $2, $3);