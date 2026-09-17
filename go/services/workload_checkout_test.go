package services

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/trace"
)

type checkoutResponses struct{ failPath, malformedPath, malformedBody string }

func (t checkoutResponses) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{}`
	switch r.URL.Path {
	case "/cart":
		body = `{"items_count":3}`
	case "/charge":
		body = `{"transaction_id":"8aee49e3-5ef8-4fcb-a0b5-73e2c2f9d628"}`
	case "/ship":
		body = `{"tracking_id":"3abdf9e7-bb1f-4140-b44c-91bfa749a57e"}`
	}
	status := http.StatusOK
	if r.URL.Path == t.failPath {
		status = http.StatusServiceUnavailable
		body = `{"error":"unavailable"}`
	}
	if r.URL.Path == t.malformedPath {
		body = t.malformedBody
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func TestCheckoutModeRequiresPaymentAndShippingEvidence(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, mode := range []string{"", "1"} {
		t.Setenv("EVAL_MODE", mode)
		for _, path := range []string{"/charge", "/ship"} {
			for _, body := range []string{`{}`, `null`, `{"transaction_id":null,"tracking_id":null}`, `{"transaction_id":12,"tracking_id":12}`} {
				http.DefaultTransport = checkoutResponses{malformedPath: path, malformedBody: body}
				server := InitCheckoutServer(":0", trace.NewNoopTracerProvider(), noop.NewLoggerProvider())
				response := httptest.NewRecorder()
				server.Handler.ServeHTTP(response, httptest.NewRequest("POST", "/checkout", nil))
				want := http.StatusOK
				if mode == "1" {
					want = http.StatusBadGateway
				}
				if response.Code != want {
					t.Fatalf("mode=%q path=%s body=%s got=%d want=%d", mode, path, body, response.Code, want)
				}
			}
		}
	}
}

func TestCheckoutModeRejectsIgnoredDependencyFailures(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, mode := range []string{"", "1"} {
		for _, failure := range []string{"", "/charge", "/recommendations", "/cart/add", "/consume", "/send"} {
			t.Setenv("EVAL_MODE", mode)
			http.DefaultTransport = checkoutResponses{failPath: failure}
			server := InitCheckoutServer(":0", trace.NewNoopTracerProvider(), noop.NewLoggerProvider())
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, httptest.NewRequest("POST", "/checkout", nil))
			want := http.StatusOK
			if mode == "1" && failure != "" {
				want = http.StatusBadGateway
			}
			if response.Code != want {
				t.Fatalf("mode=%q path=%q got=%d want=%d", mode, failure, response.Code, want)
			}
		}
	}
}
