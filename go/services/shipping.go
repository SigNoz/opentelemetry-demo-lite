package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"otel-mock/common"
	"otel-mock/config"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var (
	shippingTracer      trace.Tracer
	shippingLogger      *slog.Logger
	shippingMeter       metric.Meter
	shippingItemsCount  metric.Int64Counter
	shippingQuoteMetric metric.Float64Histogram
)

func initShippingMetrics() {
	shippingMeter = otel.Meter("shipping")
	var err error

	shippingItemsCount, err = shippingMeter.Int64Counter("app.shipping.items_count",
		metric.WithDescription("Total number of items processed for shipping"),
		metric.WithUnit("{items}"))
	if err != nil {
		panic(err)
	}

	shippingQuoteMetric, err = shippingMeter.Float64Histogram("app.shipping.quote.duration",
		metric.WithDescription("Quote calculation duration"),
		metric.WithUnit("ms"))
	if err != nil {
		panic(err)
	}
}

func RunShippingService(tp trace.TracerProvider, lp otellog.LoggerProvider) {
	shippingLogger = otelslog.NewLogger("shipping", otelslog.WithLoggerProvider(lp))
	shippingTracer = tp.Tracer("shipping")
	initShippingMetrics()

	handler := otelhttp.NewHandler(
		http.HandlerFunc(shipHandler),
		"ship",
		otelhttp.WithTracerProvider(tp),
	)

	quoteHandler := otelhttp.NewHandler(
		http.HandlerFunc(getQuoteHandler),
		"get-quote",
		otelhttp.WithTracerProvider(tp),
	)

	mux := http.NewServeMux()
	common.AddWorkloadHealth(mux, nil)
	mux.Handle("/ship", handler)
	mux.Handle("/get-quote", quoteHandler)

	port := ":8082"
	shippingLogger.Info("Shipping Service starting", "port", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		shippingLogger.Error("Shipping Service failed", "error", err)
	}
}

func shipHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	shippingLogger.InfoContext(ctx, "Processing shipping request")

	itemCount := common.WorkloadIntn(5) + 1
	quote, err := createQuoteFromCount(ctx, itemCount)
	if err != nil {
		span.RecordError(err)
		http.Error(w, "Failed to calculate quote", http.StatusInternalServerError)
		return
	}

	trackingID := uuid.New().String()

	span.SetAttributes(
		attribute.String("shipping.tracking.id", trackingID),
		attribute.Int("shipping.items.count", itemCount),
		attribute.Float64("app.shipping.cost.total", quote),
	)

	span.AddEvent("Received Quote", trace.WithAttributes(
		attribute.Float64("app.shipping.cost.total", quote),
	))

	shippingLogger.InfoContext(ctx, "Shipping successful",
		"tracking_id", trackingID,
		"items", itemCount,
		"quote", quote,
	)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"tracking_id": "%s", "cost": %.2f}`, trackingID, quote)
}

func getQuoteHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	itemCount := common.WorkloadIntn(10) + 1

	quote, err := createQuoteFromCount(ctx, itemCount)
	if err != nil {
		span.RecordError(err)
		http.Error(w, "Failed to calculate quote", http.StatusInternalServerError)
		return
	}

	span.SetAttributes(
		attribute.Int("app.quote.items.count", itemCount),
		attribute.Float64("app.quote.cost.total", quote),
	)

	shippingLogger.InfoContext(ctx, "GetQuote", "items", itemCount, "quote", quote)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"quote": %.2f, "items": %d}`, quote, itemCount)
}

func createQuoteFromCount(ctx context.Context, count int) (float64, error) {
	start := time.Now()

	ctx, span := shippingTracer.Start(ctx, "createQuoteFromCount",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	shippingLogger.InfoContext(ctx, "CreateQuoteFromCount", "items", count)

	// Record items metric
	shippingItemsCount.Add(ctx, int64(count))

	// Call external quote service (Python FastAPI) with OTel trace context propagation
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
	var requestBody io.Reader
	if common.EvalEnabled() {
		client.Transport = &workloadTransport{base: client.Transport}
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		requestBody = bytes.NewBufferString(fmt.Sprintf(`{"numberOfItems":%d}`, count))
	}
	req, err := http.NewRequestWithContext(ctx, "POST", config.QuoteURL+"/quote", requestBody)
	if err != nil {
		span.RecordError(err)
		if common.EvalEnabled() {
			return 0, err
		}
		// Fallback to local calculation
		return calculateQuoteLocally(ctx, span, count, start)
	}
	if common.EvalEnabled() {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		span.RecordError(err)
		if common.EvalEnabled() {
			return 0, err
		}
		shippingLogger.WarnContext(ctx, "QuoteService unavailable, using fallback", "error", err)
		return calculateQuoteLocally(ctx, span, count, start)
	}
	defer resp.Body.Close()
	quote := 5.99 + (float64(count) * 1.50) + float64(common.WorkloadIntn(300))/100.0
	if common.EvalEnabled() {
		var result struct {
			Cost     *float64 `json:"cost_usd"`
			Items    int      `json:"items"`
			Currency string   `json:"currency"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Cost == nil ||
			math.IsNaN(*result.Cost) || math.IsInf(*result.Cost, 0) || *result.Cost < 0 ||
			result.Items != count || result.Currency != "USD" {
			return 0, fmt.Errorf("quote service returned an invalid quote")
		}
		quote = *result.Cost
	}

	span.SetAttributes(
		attribute.Int("quote.items.count", count),
		attribute.Float64("quote.total", quote),
		attribute.Bool("quote.external_service", true),
	)

	span.AddEvent("Quote received from service", trace.WithAttributes(
		attribute.Float64("app.shipping.cost.total", quote),
	))

	shippingLogger.InfoContext(ctx, "QuoteReceived", "items", count, "quote", quote)

	duration := float64(time.Since(start).Milliseconds())
	shippingQuoteMetric.Record(ctx, duration)

	return quote, nil
}

func calculateQuoteLocally(ctx context.Context, span trace.Span, count int, start time.Time) (float64, error) {
	baseRate := 5.99
	perItemRate := 1.50
	quote := baseRate + (float64(count) * perItemRate) + float64(common.WorkloadIntn(300))/100.0

	span.SetAttributes(
		attribute.Int("quote.items.count", count),
		attribute.Float64("quote.base_rate", baseRate),
		attribute.Float64("quote.per_item_rate", perItemRate),
		attribute.Float64("quote.total", quote),
		attribute.Bool("quote.external_service", false),
	)

	span.AddEvent("Quote calculated locally", trace.WithAttributes(
		attribute.Float64("app.shipping.cost.total", quote),
	))

	shippingLogger.InfoContext(ctx, "QuoteCalculatedLocally", "items", count, "quote", quote)

	duration := float64(time.Since(start).Milliseconds())
	shippingQuoteMetric.Record(ctx, duration)

	return quote, nil
}
