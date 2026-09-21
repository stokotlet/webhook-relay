-- name: CreateEndpoint :one
INSERT INTO endpoints (id, url, secret) VALUES ($1,$2,$3) RETURNING *;

-- name: GetEndpoint :one
SELECT * FROM endpoints WHERE id = $1;

-- name: GetDelivery :one
SELECT * FROM deliveries WHERE id = $1;

-- name: GetByKey :one
SELECT * FROM deliveries WHERE idempotency_key = $1;

-- name: CreateDelivery :one
INSERT INTO deliveries (id, endpoint_id, idempotency_key, payload)
VALUES ($1,$2,$3,$4) ON CONFLICT (idempotency_key) DO NOTHING RETURNING *;

-- name: ListAttempts :many
SELECT * FROM attempts WHERE delivery_id = $1 ORDER BY number;

-- name: Claim :one
WITH candidate AS (
 SELECT id FROM deliveries
 WHERE (status = 'pending' AND next_attempt_at <= now())
 OR (status = 'processing' AND lease_until < now())
 ORDER BY next_attempt_at, created_at
 FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE deliveries d SET status = 'processing', lease_token = $1,
 lease_until = now() + interval '60 seconds', updated_at = now()
FROM candidate c WHERE d.id = c.id RETURNING d.*;

-- name: Finish :one
UPDATE deliveries SET status = $3, attempts = attempts + 1,
 next_attempt_at = $4, lease_token = NULL, lease_until = NULL, updated_at = now()
WHERE id = $1 AND lease_token = $2 AND status = 'processing'
RETURNING attempts;

-- name: RecordAttempt :exec
INSERT INTO attempts (delivery_id, number, status_code, error, duration_ms)
VALUES ($1,$2,$3,$4,$5);

-- name: Replay :one
UPDATE deliveries SET status = 'pending', max_attempts = attempts + 8,
 next_attempt_at = now(), updated_at = now()
WHERE id = $1 AND status = 'dead' RETURNING *;
