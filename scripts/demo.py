#!/usr/bin/env python3
"""Local demo: duplicate post, 503 recovery, then a dead delivery and a replay."""
import json
import os
import time
import urllib.error
import urllib.request
import uuid

API = os.getenv("DEMO_API", "http://localhost:8080")
ADMIN = os.getenv("DEMO_RECEIVER_ADMIN", "http://localhost:8091")
TARGET = os.getenv("DEMO_TARGET", "http://receiver:8090/webhooks")
KEY = os.getenv("API_KEY", "local-development-key-change-me")

def request(base, method, path, data=None, key=None):
    headers = {"Authorization": "Bearer " + KEY}
    if key:
        headers["Idempotency-Key"] = key
    body = None if data is None else json.dumps(data).encode()
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(base + path, body, headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            raw = response.read()
            return response.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"{method} {path}: {error.code} {error.read().decode()}") from error

def wait(delivery_id, expected):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        _, state = request(API, "GET", "/v1/deliveries/" + delivery_id)
        if state["status"] == expected:
            return state
        time.sleep(0.5)
    raise RuntimeError(f"delivery never reached {expected}: {state}")

def main():
    _, endpoint = request(API, "POST", "/v1/endpoints", {"url": TARGET})
    secret = endpoint["secret"]
    request(ADMIN, "POST", "/configure", {"secret": secret, "failures": 2})
    event = {"endpoint_id": endpoint["id"], "payload": {"type": "order.paid", "order_id": 123}}
    key = str(uuid.uuid4())
    status, first = request(API, "POST", "/v1/events", event, key)
    assert status == 202
    status, duplicate = request(API, "POST", "/v1/events", event, key)
    assert status == 200 and first["id"] == duplicate["id"]
    print("Event saved; duplicate submission returned the same delivery.")
    state = wait(first["id"], "delivered")
    assert [a["status_code"] for a in state["attempts"]] == [503, 503, 204], state
    print("Temporary outage recovered: 503 -> 503 -> 204.")
    request(ADMIN, "POST", "/configure", {"secret": secret, "status": 400})
    _, second = request(API, "POST", "/v1/events", event, str(uuid.uuid4()))
    wait(second["id"], "dead")
    request(ADMIN, "POST", "/configure", {"secret": secret, "status": 204})
    request(API, "POST", "/v1/deliveries/" + second["id"] + "/retry")
    state = wait(second["id"], "delivered")
    assert [a["status_code"] for a in state["attempts"]] == [400, 204], state
    print("Permanent failure and manual replay: 400 -> dead -> retry -> 204.")
    print("Demo passed. Inspect delivery:", API + "/v1/deliveries/" + second["id"])

if __name__ == "__main__":
    main()
