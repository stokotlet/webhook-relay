package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stokotlet/webhook-relay/internal/delivery"
	"github.com/stokotlet/webhook-relay/internal/metrics"
	"github.com/stokotlet/webhook-relay/internal/store"
	"github.com/stokotlet/webhook-relay/internal/store/db"
)

type Queue interface {
	ClaimDelivery(context.Context) (db.Delivery, error)
	GetEndpoint(context.Context, string) (db.Endpoint, error)
	Complete(context.Context, db.Delivery, store.Result) error
}
type Sender interface {
	Send(context.Context, string, string, string, []byte) delivery.Outcome
}
type Worker struct {
	Queue   Queue
	Sender  Sender
	Metrics *metrics.Metrics
	Log     *slog.Logger
}

func (w *Worker) Run(ctx context.Context, count int) {
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() { defer wg.Done(); w.loop(ctx) }()
	}
	wg.Wait()
}
func (w *Worker) loop(ctx context.Context) {
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		d, err := w.Queue.ClaimDelivery(claimCtx)
		cancel()
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
				w.Log.Error("claim failed", "error", err)
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		w.process(ctx, d)
	}
}
func (w *Worker) process(ctx context.Context, d db.Delivery) {
	w.Metrics.Active.Inc()
	defer w.Metrics.Active.Dec()
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	endpoint, err := w.Queue.GetEndpoint(lookupCtx, d.EndpointID)
	cancel()
	if err != nil {
		w.Log.Error("endpoint lookup failed", "delivery_id", d.ID, "error", err)
		return
	}
	outcome := w.Sender.Send(ctx, endpoint.Url, endpoint.Secret, d.ID, d.Payload)
	result := Decision(d, outcome, time.Now())
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer saveCancel()
	if err = w.Queue.Complete(saveCtx, d, result); err != nil {
		w.Log.Error("save attempt failed", "delivery_id", d.ID, "error", err)
		return
	}
	w.Metrics.Attempts.WithLabelValues(result.Status).Inc()
	w.Metrics.Duration.Observe(outcome.Duration.Seconds())
	w.Log.Info("delivery attempted", "delivery_id", d.ID, "status", result.Status, "http_status", outcome.Code, "attempt", d.Attempts+1)
}
func Decision(d db.Delivery, o delivery.Outcome, now time.Time) store.Result {
	r := store.Result{Status: "dead", Code: o.Code, Error: o.Error, Duration: o.Duration, Next: now}
	if o.Code >= 200 && o.Code < 300 {
		r.Status = "delivered"
		return r
	}
	if delivery.Retryable(o.Code) && d.Attempts+1 < d.MaxAttempts {
		r.Status = "pending"
		r.Next = now.Add(max(delivery.Backoff(int(d.Attempts)+1), o.RetryAfter))
	}
	return r
}
