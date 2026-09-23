package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/stokotlet/webhook-relay/internal/delivery"
	"github.com/stokotlet/webhook-relay/internal/store"
	"github.com/stokotlet/webhook-relay/internal/store/db"
)

type API struct {
	Store        *store.Store
	Key          string
	AllowPrivate bool
	Log          *slog.Logger
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/endpoints", a.createEndpoint)
	mux.HandleFunc("POST /v1/events", a.enqueue)
	mux.HandleFunc("GET /v1/deliveries/{id}", a.getDelivery)
	mux.HandleFunc("POST /v1/deliveries/{id}/retry", a.replay)
	want := sha256.Sum256([]byte("Bearer " + a.Key))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			problem(w, 401, "unauthorized")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		problem(w, 415, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err = dec.Decode(v); err == nil {
		var extra any
		if dec.Decode(&extra) != io.EOF {
			err = errors.New("trailing JSON")
		}
	}
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			problem(w, 413, "request too large")
		} else {
			problem(w, 400, "invalid JSON request")
		}
		return false
	}
	return true
}
func (a *API) createEndpoint(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.URL) > 2048 {
		problem(w, 400, "destination URL too long")
		return
	}
	if err := delivery.ValidateURL(input.URL, a.AllowPrivate); err != nil {
		problem(w, 400, err.Error())
		return
	}
	endpoint, err := a.Store.CreateEndpoint(r.Context(), db.CreateEndpointParams{ID: store.ID(), Url: input.URL, Secret: store.ID() + store.ID()})
	if err != nil {
		a.failure(w, err)
		return
	}
	respond(w, 201, map[string]any{"id": endpoint.ID, "url": endpoint.Url, "secret": endpoint.Secret})
}
func (a *API) enqueue(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		problem(w, 400, "Idempotency-Key must contain 1 to 128 characters")
		return
	}
	var input struct {
		EndpointID string          `json:"endpoint_id"`
		Payload    json.RawMessage `json:"payload"`
	}
	if !decode(w, r, &input) {
		return
	}
	body := strings.TrimSpace(string(input.Payload))
	if len(body) == 0 || body[0] != '{' {
		problem(w, 400, "payload must be a JSON object")
		return
	}
	_, err := a.Store.GetEndpoint(r.Context(), input.EndpointID)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "endpoint not found")
		return
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	d, created, err := a.Store.Enqueue(r.Context(), input.EndpointID, key, input.Payload)
	if errors.Is(err, store.ErrConflict) {
		problem(w, 409, err.Error())
		return
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	code := 200
	if created {
		code = 202
	}
	w.Header().Set("Location", "/v1/deliveries/"+d.ID)
	respond(w, code, view(d))
}
func view(d db.Delivery) map[string]any {
	return map[string]any{"id": d.ID, "endpoint_id": d.EndpointID, "status": d.Status, "attempt_count": d.Attempts, "max_attempts": d.MaxAttempts, "next_attempt_at": d.NextAttemptAt, "created_at": d.CreatedAt}
}
func (a *API) getDelivery(w http.ResponseWriter, r *http.Request) {
	d, err := a.Store.GetDelivery(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "delivery not found")
		return
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	history, err := a.Store.ListAttempts(r.Context(), d.ID)
	if err != nil {
		a.failure(w, err)
		return
	}
	output := view(d)
	output["attempts"] = history
	respond(w, 200, output)
}
func (a *API) replay(w http.ResponseWriter, r *http.Request) {
	d, err := a.Store.Replay(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		_, lookup := a.Store.GetDelivery(r.Context(), r.PathValue("id"))
		if errors.Is(lookup, pgx.ErrNoRows) {
			problem(w, 404, "delivery not found")
		} else if lookup != nil {
			a.failure(w, lookup)
		} else {
			problem(w, 409, "only dead deliveries can be retried")
		}
		return
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	respond(w, 202, view(d))
}
func (a *API) failure(w http.ResponseWriter, err error) {
	a.Log.Error("API operation failed", "error", err)
	problem(w, 500, "internal server error")
}
func problem(w http.ResponseWriter, code int, message string) {
	respond(w, code, map[string]string{"error": message})
}
func respond(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
