package delivery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Sender struct {
	client       *http.Client
	allowPrivate bool
}
type Outcome struct {
	Code       int
	Error      string
	Duration   time.Duration
	RetryAfter time.Duration
}

func NewSender(allowPrivate bool) *Sender {
	transport := &http.Transport{DialContext: safeDial(allowPrivate), MaxIdleConns: 100, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	return &Sender{allowPrivate: allowPrivate, client: &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (s *Sender) Close() { s.client.CloseIdleConnections() }
func (s *Sender) Send(ctx context.Context, url, secret, id string, payload []byte) Outcome {
	start := time.Now()
	if err := ValidateURL(url, s.allowPrivate); err != nil {
		return Outcome{Error: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Outcome{Error: "invalid destination"}
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "WebhookRelay/1.0")
	req.Header.Set("Webhook-Id", id)
	req.Header.Set("Webhook-Timestamp", timestamp)
	req.Header.Set("Webhook-Signature", Signature(secret, id, timestamp, payload))
	resp, err := s.client.Do(req)
	if err != nil {
		return Outcome{Error: "HTTP request failed", Duration: time.Since(start)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	result := Outcome{Code: resp.StatusCode, Duration: time.Since(start), RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return result
}
func Retryable(code int) bool { return code == 0 || code == 408 || code == 429 || code >= 500 }
func Backoff(attempt int) time.Duration {
	attempt = max(1, min(attempt, 10))
	base := min(5*time.Second*time.Duration(1<<uint(attempt-1)), time.Hour)
	return base/2 + time.Duration(rand.Int64N(int64(base/2)+1))
}
func parseRetryAfter(value string, now time.Time) time.Duration {
	var d time.Duration
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil {
		d = time.Duration(seconds) * time.Second
	} else if date, err := http.ParseTime(value); err == nil {
		d = date.Sub(now)
	}
	return min(max(d, 0), 24*time.Hour)
}
