package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stokotlet/webhook-relay/internal/store/db"
)

var ErrConflict = errors.New("idempotency key was used for a different event")
var ErrLeaseLost = errors.New("delivery lease lost")

type Store struct {
	Pool *pgxpool.Pool
	*db.Queries
}

func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 16
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "3000"
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return &Store{Pool: p, Queries: db.New(p)}, nil
}

func ID() string { return rand.Text() }

func Canonical(payload []byte) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// ON CONFLICT DO NOTHING returns no row. The select below sees the other insert.
func (s *Store) Enqueue(ctx context.Context, endpoint, key string, payload []byte) (db.Delivery, bool, error) {
	canonical, err := Canonical(payload)
	if err != nil {
		return db.Delivery{}, false, err
	}
	d, err := s.CreateDelivery(ctx, db.CreateDeliveryParams{ID: ID(), EndpointID: endpoint, IdempotencyKey: key, Payload: canonical})
	if err == nil {
		return d, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return d, false, err
	}
	d, err = s.GetByKey(ctx, key)
	if err != nil {
		return d, false, err
	}
	previous, err := Canonical(d.Payload)
	if err != nil {
		return d, false, err
	}
	if d.EndpointID != endpoint || !bytes.Equal(previous, canonical) {
		return d, false, ErrConflict
	}
	return d, false, nil
}

func (s *Store) ClaimDelivery(ctx context.Context) (db.Delivery, error) {
	return s.Claim(ctx, pgtype.Text{String: ID(), Valid: true})
}

type Result struct {
	Status   string
	Code     int
	Error    string
	Duration time.Duration
	Next     time.Time
}

func (s *Store) Complete(ctx context.Context, d db.Delivery, r Result) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.WithTx(tx)
	number, err := q.Finish(ctx, db.FinishParams{ID: d.ID, LeaseToken: d.LeaseToken, Status: r.Status, NextAttemptAt: pgtype.Timestamptz{Time: r.Next, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	err = q.RecordAttempt(ctx, db.RecordAttemptParams{DeliveryID: d.ID, Number: number, StatusCode: int32(r.Code), Error: r.Error, DurationMs: r.Duration.Milliseconds()})
	if err != nil {
		return fmt.Errorf("record attempt: %w", err)
	}
	return tx.Commit(ctx)
}
