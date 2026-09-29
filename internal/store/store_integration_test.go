//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stokotlet/webhook-relay/internal/api"
	"github.com/stokotlet/webhook-relay/internal/delivery"
	"github.com/stokotlet/webhook-relay/internal/metrics"
	"github.com/stokotlet/webhook-relay/internal/store"
	"github.com/stokotlet/webhook-relay/internal/store/db"
	"github.com/stokotlet/webhook-relay/internal/worker"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func database(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		container, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("relay"), postgres.WithUsername("relay"), postgres.WithPassword("relay"), postgres.BasicWaitStrategies())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		url, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Migrate(ctx, url); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Pool.Close)
	// TEST_DATABASE_URL must point to a disposable database.
	if _, err = s.Pool.Exec(ctx, "TRUNCATE attempts, deliveries, endpoints CASCADE"); err != nil {
		t.Fatal(err)
	}
	return s
}
func endpoint(t *testing.T, s *store.Store, url string) db.Endpoint {
	t.Helper()
	e, err := s.CreateEndpoint(context.Background(), db.CreateEndpointParams{ID: store.ID(), Url: url, Secret: "integration-secret"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestQueueTransactionsAndLeases(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	e := endpoint(t, s, "https://example.com/webhooks")
	var wg sync.WaitGroup
	var created atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, fresh, err := s.Enqueue(ctx, e.ID, "same", []byte(`{"a":1,"b":2}`))
			if err != nil {
				t.Error(err)
			}
			if fresh {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created %d deliveries", created.Load())
	}
	if _, _, err := s.Enqueue(ctx, e.ID, "same", []byte(`{"b":2, "a":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Enqueue(ctx, e.ID, "same", []byte(`{"a":2}`)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	var claims atomic.Int32
	var first db.Delivery
	var mu sync.Mutex
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.ClaimDelivery(ctx)
			if err == nil {
				claims.Add(1)
				mu.Lock()
				first = d
				mu.Unlock()
			} else if !errors.Is(err, pgx.ErrNoRows) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("claimed %d times", claims.Load())
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE deliveries SET lease_until=now()-interval '1 second' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := s.ClaimDelivery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	success := store.Result{Status: "delivered", Code: 204, Next: time.Now()}
	if err = s.Complete(ctx, first, success); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("stale lease: %v", err)
	}
	if err = s.Complete(ctx, reclaimed, success); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(ctx, reclaimed, success); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("duplicate completion: %v", err)
	}
	history, err := s.ListAttempts(ctx, first.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %v, %v", history, err)
	}
	// attempt insert fails, delivery status should stay put
	d, _, err := s.Enqueue(ctx, e.ID, "rollback", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDelivery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAttempt(ctx, db.RecordAttemptParams{DeliveryID: d.ID, Number: 1}); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(ctx, claimed, success); err == nil {
		t.Fatal("expected duplicate attempt failure")
	}
	unchanged, err := s.GetDelivery(ctx, d.ID)
	if err != nil || unchanged.Status != "processing" || unchanged.Attempts != 0 {
		t.Fatalf("transaction did not roll back: %+v %v", unchanged, err)
	}
}
func TestAPIWorkerLifecycle(t *testing.T) {
	s := database(t)
	var calls atomic.Int32
	var code atomic.Int32
	code.Store(503)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !delivery.Verify("integration-secret", r.Header.Get("Webhook-Id"), r.Header.Get("Webhook-Timestamp"), body, r.Header.Get("Webhook-Signature")) {
			t.Error("invalid signature")
		}
		calls.Add(1)
		w.WriteHeader(int(code.Load()))
	}))
	defer receiver.Close()
	e := endpoint(t, s, receiver.URL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := (&api.API{Store: s, Key: "test-key", AllowPrivate: true, Log: logger}).Handler()
	request := func(method, path, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test-key")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	body := `{"endpoint_id":"` + e.ID + `","payload":{"order":123}}`
	response := request("POST", "/v1/events", body, "order-123")
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	var accepted struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if r := request("POST", "/v1/events", body, "order-123"); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	sender := delivery.NewSender(true)
	defer sender.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	w := worker.Worker{Queue: s, Sender: sender, Metrics: metrics.New(prometheus.NewRegistry()), Log: logger}
	go func() { defer close(done); w.Run(ctx, 4) }()
	defer func() { cancel(); <-done }()
	wait := func(status string, attempts int32) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			d, err := s.GetDelivery(context.Background(), accepted.ID)
			if err == nil && d.Status == status && d.Attempts == attempts {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("did not reach %s with %d attempts", status, attempts)
	}
	wait("pending", 1)
	code.Store(400)
	if _, err := s.Pool.Exec(context.Background(), "UPDATE deliveries SET next_attempt_at=now() WHERE id=$1", accepted.ID); err != nil {
		t.Fatal(err)
	}
	wait("dead", 2)
	code.Store(204)
	if r := request("POST", "/v1/deliveries/"+accepted.ID+"/retry", "", ""); r.Code != 202 {
		t.Fatal(r.Body.String())
	}
	wait("delivered", 3)
	if r := request("POST", "/v1/deliveries/"+accepted.ID+"/retry", "", ""); r.Code != 409 {
		t.Fatal("replayed delivered event")
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected requests: %d", calls.Load())
	}
	if r := request("GET", "/v1/deliveries/"+accepted.ID, "", ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"status_code":503`) {
		t.Fatal(r.Body.String())
	}
}
