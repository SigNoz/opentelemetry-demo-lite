package services

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestWorkloadQuoteRequiresActualQuote(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	t.Setenv("EVAL_MODE", "1")
	shippingTracer = trace.NewNoopTracerProvider().Tracer("shipping")
	shippingLogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	initShippingMetrics()
	for _, body := range []string{
		`{"cost_usd":8.99,"items":2,"currency":"USD"}`,
		`{}`, `{"error":"unavailable"}`, `not-json`,
		`{"cost_usd":8.99,"items":1,"currency":"USD"}`,
		`{"cost_usd":-1,"items":2,"currency":"USD"}`,
	} {
		http.DefaultTransport = &responseTransport{status: http.StatusOK, body: body}
		quote, err := createQuoteFromCount(context.Background(), 2)
		if body == `{"cost_usd":8.99,"items":2,"currency":"USD"}` {
			if err != nil || quote != 8.99 {
				t.Fatalf("quote=%v error=%v", quote, err)
			}
		} else if err == nil {
			t.Fatalf("accepted unusable quote %q", body)
		}
	}
}
