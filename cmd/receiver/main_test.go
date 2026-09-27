package main

import (
	"github.com/stokotlet/webhook-relay/internal/delivery"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReceiverFailureDeduplicationAndReplayWindow(t *testing.T) {
	f := &fixture{secret: "secret", failures: 1, seen: map[string]bool{}}
	send := func(ts string) int {
		body := `{"order":123}`
		r := httptest.NewRequest("POST", "/webhooks", strings.NewReader(body))
		r.Header.Set("Webhook-Id", "event")
		r.Header.Set("Webhook-Timestamp", ts)
		r.Header.Set("Webhook-Signature", delivery.Signature("secret", "event", ts, []byte(body)))
		w := httptest.NewRecorder()
		f.webhook(w, r)
		return w.Code
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if send(now) != 503 || send(now) != 204 || send(now) != 204 {
		t.Fatal("unexpected delivery statuses")
	}
	if f.accepted != 1 || f.attempts != 3 {
		t.Fatal("duplicate applied twice")
	}
	if send(strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)) != 401 {
		t.Fatal("stale signed message accepted")
	}
}
