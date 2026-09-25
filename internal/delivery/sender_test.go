package delivery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestSignatureIntegrity(t *testing.T) {
	body := []byte(`{"order":123}`)
	sig := Signature("secret", "id", "1234", body)
	if !Verify("secret", "id", "1234", body, sig) {
		t.Fatal("valid signature rejected")
	}
	for _, tc := range []struct {
		secret, id, ts string
		body           []byte
	}{{"wrong", "id", "1234", body}, {"secret", "other", "1234", body}, {"secret", "id", "1235", body}, {"secret", "id", "1234", []byte(`{}`)}} {
		if Verify(tc.secret, tc.id, tc.ts, tc.body, sig) {
			t.Fatal("tampering accepted")
		}
	}
}
func TestSenderSignsAndDoesNotRedirect(t *testing.T) {
	secret := "shared-secret"
	body := []byte(`{"order":123}`)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed redirect") }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if string(got) != string(body) || !Verify(secret, r.Header.Get("Webhook-Id"), r.Header.Get("Webhook-Timestamp"), got, r.Header.Get("Webhook-Signature")) {
			t.Error("bad signed body")
		}
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(302)
	}))
	defer server.Close()
	sender := NewSender(true)
	defer sender.Close()
	result := sender.Send(context.Background(), server.URL, secret, "event-123", body)
	if result.Code != 302 || Retryable(result.Code) {
		t.Fatalf("unexpected result: %+v", result)
	}
}
func TestTargetRestrictions(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "::ffff:127.0.0.1", "100.64.0.1", "2001:db8::1", "64:ff9b::7f00:1"} {
		if publicIP(netip.MustParseAddr(address)) {
			t.Errorf("allowed %s", address)
		}
	}
	if !publicIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public IP rejected")
	}
	for _, url := range []string{"http://example.com", "https://user:pass@example.com", "https://127.0.0.1", "https://example.com/#x"} {
		if ValidateURL(url, false) == nil {
			t.Errorf("allowed %s", url)
		}
	}
	_, err := safeDial(false)(context.Background(), "tcp", "localhost:80")
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("private DNS destination not rejected: %v", err)
	}
}
func TestBackoffAndRetryAfter(t *testing.T) {
	for attempt := 1; attempt <= 8; attempt++ {
		for range 20 {
			delay := Backoff(attempt)
			base := 5 * time.Second * time.Duration(1<<uint(attempt-1))
			if delay < base/2 || delay > base {
				t.Fatalf("out of range: %v", delay)
			}
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{{"30", 30 * time.Second}, {"-1", 0}, {"junk", 0}, {"999999", 24 * time.Hour}, {now.Add(time.Minute).Format(http.TimeFormat), time.Minute}} {
		if got := parseRetryAfter(tc.value, now); got != tc.want {
			t.Errorf("%q: %v", tc.value, got)
		}
	}
}
