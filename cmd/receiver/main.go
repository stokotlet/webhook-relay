// Demo receiver used by compose. Not part of the relay itself.
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/stokotlet/webhook-relay/internal/delivery"
)

type fixture struct {
	sync.Mutex
	secret                               string
	failures, status, attempts, accepted int
	seen                                 map[string]bool
}

func (f *fixture) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	f.Lock()
	defer f.Unlock()
	timestamp := r.Header.Get("Webhook-Timestamp")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	now := time.Now().Unix()
	if err != nil || seconds < now-300 || seconds > now+300 || f.secret == "" || !delivery.Verify(f.secret, r.Header.Get("Webhook-Id"), timestamp, body, r.Header.Get("Webhook-Signature")) {
		http.Error(w, "invalid signature or timestamp", http.StatusUnauthorized)
		return
	}
	f.attempts++
	if f.failures > 0 {
		f.failures--
		http.Error(w, "temporary outage", http.StatusServiceUnavailable)
		return
	}
	if f.status != 0 && f.status != 204 {
		http.Error(w, "configured failure", f.status)
		return
	}
	id := r.Header.Get("Webhook-Id")
	if !f.seen[id] {
		f.seen[id] = true
		f.accepted++
	}
	w.WriteHeader(204)
}
func (f *fixture) configure(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Secret   string `json:"secret"`
		Failures int    `json:"failures"`
		Status   int    `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil || input.Secret == "" || input.Failures < 0 || (input.Status != 0 && (input.Status < 200 || input.Status > 599)) {
		http.Error(w, "invalid configuration", http.StatusBadRequest)
		return
	}
	f.Lock()
	defer f.Unlock()
	f.secret = input.Secret
	f.failures = input.Failures
	f.status = input.Status
	w.WriteHeader(204)
}
func (f *fixture) stats(w http.ResponseWriter, r *http.Request) {
	f.Lock()
	defer f.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"attempts": f.attempts, "accepted": f.accepted})
}
func main() {
	f := &fixture{seen: make(map[string]bool)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks", f.webhook)
	admin := http.NewServeMux()
	admin.HandleFunc("POST /configure", f.configure)
	admin.HandleFunc("GET /stats", f.stats)
	addr := os.Getenv("RECEIVER_ADMIN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8091"
	}
	go func() {
		log.Fatal((&http.Server{Addr: addr, Handler: admin, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}).ListenAndServe())
	}()
	log.Fatal((&http.Server{Addr: ":8090", Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}).ListenAndServe())
}
