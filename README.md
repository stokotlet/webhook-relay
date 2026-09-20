# Webhook Relay

A Go service that accepts events and delivers signed webhooks with durable retries.
PostgreSQL owns the delivery queue, leases, and attempt history.

## Development

Requires Go 1.26 and PostgreSQL 17. Docker Compose provides the demo environment.

```sh
cp .env.example .env
make test
```

Implementation and demo instructions are added alongside each feature.
