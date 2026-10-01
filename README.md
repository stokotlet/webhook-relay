# webhook-relay

Stores an incoming webhook in Postgres and keeps retrying until the receiver returns 2xx or the attempt budget runs out. Postgres is the queue, there is no Redis.

## Demo

Needs Docker Compose, Python 3 and Make. Uses ports 8080, 8091 and 9090, bound to localhost.

```sh
docker compose up --build -d --wait
make demo
```

The script posts one event twice with the same idempotency key, waits through two 503s, then sends a 400, replays it, and expects 204. Don't run two demos at once: the receiver keeps a single secret.

```sh
docker compose --profile observability up -d
docker compose --profile observability down
```

Grafana: http://localhost:3000 (`admin` / `local-demo-only`). Prometheus: http://localhost:9091. Relay metrics: http://localhost:9090/metrics.

## API

```sh
export API_KEY=local-development-key-change-me
curl -sS localhost:8080/v1/endpoints \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://your-service.example/webhooks"}'
```

Then, with the endpoint id from that response:

```sh
curl -i localhost:8080/v1/events \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: order-123-paid' \
  -d '{"endpoint_id":"ENDPOINT_ID","payload":{"type":"order.paid","order_id":123}}'
```

- `POST /v1/endpoints` — save a URL, secret comes back once
- `POST /v1/events` — save the event and queue it
- `GET /v1/deliveries/{id}` — status and attempts
- `POST /v1/deliveries/{id}/retry` — only works when status is `dead`

`/healthz`, `/readyz` and `/metrics` are on the admin port. That listener is not authenticated.

Contract: [docs/openapi.json](docs/openapi.json). Delivery notes: [docs/architecture.md](docs/architecture.md).

## Local run

Go 1.26.5+, Postgres 17+. The process does not read `.env` by itself.

```sh
cp .env.example .env
set -a; . ./.env; set +a
go run ./cmd/relay -mode migrate
go run ./cmd/relay
```

| Variable | |
| --- | --- |
| `DATABASE_URL` | required |
| `API_KEY` | required, at least 24 characters |
| `HTTP_ADDR` | `:8080` |
| `ADMIN_ADDR` | `127.0.0.1:9090` |
| `WORKERS` | `4`, range 1–64 |
| `ALLOW_PRIVATE_TARGETS` | `false`. Compose turns it on for the demo receiver |

```sh
make test          # go test -race
make lint          # go vet
make build
make integration   # Testcontainers, or TEST_DATABASE_URL
make generate      # sqlc
```

`TEST_DATABASE_URL` must be a database you can wipe. The integration tests truncate `attempts`, `deliveries` and `endpoints`.
