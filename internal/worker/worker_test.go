package worker

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stokotlet/webhook-relay/internal/delivery"
	"github.com/stokotlet/webhook-relay/internal/metrics"
	"github.com/stokotlet/webhook-relay/internal/store"
	"github.com/stokotlet/webhook-relay/internal/store/db"
	"io"
	"log/slog"
	"sync/atomic"
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

type fakeQueue struct {
	polls     atomic.Int32
	completed chan store.Result
	d         db.Delivery
	failSave  bool
}

func (q *fakeQueue) ClaimDelivery(context.Context) (db.Delivery, error) {
	if q.polls.Add(1) == 1 {
		return q.d, nil
	}
	return db.Delivery{}, pgx.ErrNoRows
}
func (q *fakeQueue) GetEndpoint(context.Context, string) (db.Endpoint, error) {
	return db.Endpoint{Url: "https://example.com", Secret: "secret"}, nil
}
func (q *fakeQueue) Complete(_ context.Context, _ db.Delivery, r store.Result) error {
	q.completed <- r
	if q.failSave {
		return errors.New("database unavailable")
	}
	return nil
}

type fakeSender struct{}

func (fakeSender) Send(context.Context, string, string, string, []byte) delivery.Outcome {
	return delivery.Outcome{Code: 503, Error: "HTTP 503"}
}
func TestWorkerPersistsOutcomeAndStopsWhileIdle(t *testing.T) {
	q := &fakeQueue{completed: make(chan store.Result, 1), d: db.Delivery{ID: "event", MaxAttempts: 8}}
	w := Worker{Queue: q, Sender: fakeSender{}, Metrics: metrics.New(prometheus.NewRegistry()), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx, 1) }()
	select {
	case result := <-q.completed:
		if result.Status != "pending" || result.Code != 503 {
			t.Fatalf("unexpected result: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not persist result")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle worker ignored cancellation")
	}
	if q.polls.Load() > 2 {
		t.Fatal("worker busy-polled the queue")
	}
}
func TestWorkerDoesNotCountUncommittedAttempt(t *testing.T) {
	q := &fakeQueue{completed: make(chan store.Result, 1), failSave: true}
	registry := prometheus.NewRegistry()
	stats := metrics.New(registry)
	w := Worker{Queue: q, Sender: fakeSender{}, Metrics: stats, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w.process(context.Background(), db.Delivery{ID: "event", MaxAttempts: 8})
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "relay_delivery_attempts_total" {
			t.Fatal("uncommitted outcome counted")
		}
		if family.GetName() == "relay_active_workers" && family.Metric[0].GetGauge().GetValue() != 0 {
			t.Fatal("active gauge leaked")
		}
	}
}
