# Delivery

`POST /v1/events` inserts one row and returns 202. A worker claims that row, POSTs the body, and writes the outcome back. The body and the schedule live in the same row, so there is no separate "event saved but job missing" state.

## Rows

`endpoints` is the URL and the signing secret.

`deliveries` is the body, the endpoint, a unique idempotency key, status and the lease. This version sends each event to one endpoint.

`attempts` is a finished HTTP call: status code, a short error string, duration.

Same idempotency key and the same JSON object returns the existing delivery with 200. A different endpoint or a different object is 409. Object key order and whitespace are ignored. `1` and `1.0` are not the same. The key stays until the delivery row is deleted. Nothing deletes old rows yet.

## Claim

One statement: `FOR UPDATE SKIP LOCKED`, set a random `lease_token`, `lease_until = now() + 60 seconds`. The transaction commits before the HTTP call, so a slow receiver does not hold a row lock.

Idle workers sleep one second. The pending and expired-lease indexes exist so the poll does not read payloads. The pool is capped at 16 connections.

When the lease expires another worker can take the row. `Finish` updates only if `lease_token` still matches, so the first worker cannot overwrite the second. The status change and the attempt insert commit together.

## Status

`pending` to `processing` on claim. `processing` to `delivered` on 2xx. Back to `pending` when the error is retryable and attempts remain. `dead` on anything else, or when the budget is spent. Manual retry moves `dead` back to `pending` and adds 8 attempts. The event id and the old attempts stay.

Timeout on the outbound call is 10 seconds. Retried: DNS and connection errors, 408, 429, 5xx. Not retried: other non-2xx, including redirects. Backoff starts in the 2.5–5s range and doubles, with jitter. `Retry-After` can push the next try out, but not past 24 hours. The default budget is 8 completed attempts.

A crash before the result is saved does not increment `attempts`. The receiver may already have seen the request. If the process keeps dying, the number of HTTP calls can be higher than 8.

## Limits

Delivery is at-least-once, inside that budget. A receiver that never comes back stays `dead`. Duplicates are possible if they committed and the response was lost, so they should dedupe on `Webhook-Id`. 2xx means they accepted the request. Order between events is not kept.

If the sender needs "row saved and webhook submitted" to be one transaction, that has to happen on their side. This service only sees what it was given.

## Outbound calls

API auth is one bearer key. URLs have to be https, unless `ALLOW_PRIVATE_TARGETS` is set (the compose file sets it). After DNS, private and reserved addresses are rejected and the checked IP is dialed directly. Redirects are off, and the process does not use `HTTP_PROXY`.

The signature is HMAC-SHA256 of `id.timestamp.raw_body`, sent as `v1=<hex>`. The demo receiver drops timestamps outside five minutes. The secret is returned only from endpoint creation and is stored in Postgres as plain text. Response bodies and the `Authorization` header are not logged. Metric labels are the result, not the URL or the event id.

## Process

`-mode all` runs API and workers together. `-mode api` and `-mode worker` split them. `-mode migrate` applies the goose migrations and exits. On SIGTERM the servers stop accepting, in-flight requests are cancelled, and workers get a few seconds to save. If that save fails, the lease expiry picks the job up later.
