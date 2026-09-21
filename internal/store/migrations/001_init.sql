-- +goose Up
CREATE TABLE endpoints (
 id text PRIMARY KEY,
 url text NOT NULL,
 secret text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE deliveries (
 id text PRIMARY KEY,
 endpoint_id text NOT NULL REFERENCES endpoints(id),
 idempotency_key text NOT NULL UNIQUE,
 payload jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','delivered','dead')),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 max_attempts integer NOT NULL DEFAULT 8,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_token text,
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deliveries_pending ON deliveries(next_attempt_at, created_at) WHERE status = 'pending';
CREATE INDEX deliveries_leases ON deliveries(lease_until) WHERE status = 'processing';
CREATE TABLE attempts (
 delivery_id text NOT NULL REFERENCES deliveries(id),
 number integer NOT NULL,
 status_code integer NOT NULL,
 error text NOT NULL,
 duration_ms bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (delivery_id, number)
);

-- +goose Down
DROP TABLE attempts;
DROP TABLE deliveries;
DROP TABLE endpoints;
