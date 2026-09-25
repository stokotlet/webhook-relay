package worker

import (
	"github.com/stokotlet/webhook-relay/internal/delivery"
	"github.com/stokotlet/webhook-relay/internal/store/db"
	"testing"
	"time"
)

func TestDecision(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		code     int
		attempts int32
		want     string
	}{{"success", 204, 0, "delivered"}, {"network", 0, 0, "pending"}, {"rate limit", 429, 0, "pending"}, {"timeout", 408, 0, "pending"}, {"server", 503, 0, "pending"}, {"bad request", 400, 0, "dead"}, {"redirect", 302, 0, "dead"}, {"exhausted", 503, 7, "dead"}, {"last success", 200, 7, "delivered"}} {
		t.Run(tc.name, func(t *testing.T) {
			r := Decision(db.Delivery{Attempts: tc.attempts, MaxAttempts: 8}, delivery.Outcome{Code: tc.code, RetryAfter: time.Hour}, now)
			if r.Status != tc.want {
				t.Fatalf("got %s", r.Status)
			}
			if r.Status == "pending" && r.Next.Before(now.Add(time.Hour)) {
				t.Fatal("ignored Retry-After")
			}
		})
	}
}
