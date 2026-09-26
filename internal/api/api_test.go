package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthentication(t *testing.T) {
	a := &API{Key: "correct", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, key := range []string{"", "Bearer wrong", "correct"} {
		r := httptest.NewRequest("GET", "/v1/deliveries/x", nil)
		r.Header.Set("Authorization", key)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("unauthorized status %d", w.Code)
		}
	}
}
func TestDecodeRejectsMalformedRequests(t *testing.T) {
	for _, tc := range []struct {
		name, body, content string
		want                int
	}{{"trailing", `{"url":"x"} {}`, "application/json", 400}, {"unknown", `{"extra":true}`, "application/json", 400}, {"type", `{}`, "text/plain", 415}, {"large", `{"url":"` + strings.Repeat("a", 256<<10) + `"}`, "application/json", 413}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.content)
			w := httptest.NewRecorder()
			var input struct {
				URL string `json:"url"`
			}
			if decode(w, r, &input) || w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
}
