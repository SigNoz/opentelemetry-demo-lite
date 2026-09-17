package services

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand"
	"net/http"
	"otel-mock/common"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var (
	accountingTracer trace.Tracer
	accountingMeter  metric.Meter
	accountingLogger *slog.Logger
)

var (
	ordersProcessed metric.Int64Counter
	revenueTotal    metric.Float64Counter
)

func InitAccountingService(port string, tp trace.TracerProvider, mp metric.MeterProvider, lp otellog.LoggerProvider) *http.Server {
	accountingTracer = tp.Tracer("accounting")
	accountingMeter = mp.Meter("accounting")

	var err error
	ordersProcessed, err = accountingMeter.Int64Counter("app.accounting.orders_processed",
		metric.WithDescription("Total orders processed by accounting"),
		metric.WithUnit("{orders}"))
	if err != nil {
		slog.Error("Failed to create orders_processed counter", "error", err)
	}

	revenueTotal, err = accountingMeter.Float64Counter("app.accounting.revenue_total",
		metric.WithDescription("Total revenue processed"),
		metric.WithUnit("USD"))
	if err != nil {
		slog.Error("Failed to create revenue_total counter", "error", err)
	}

	mux := http.NewServeMux()
	// Wrap with otelhttp to extract trace context from incoming requests
	mux.Handle("/consume", otelhttp.NewHandler(
		http.HandlerFunc(handleAccountingConsume),
		"orders receive",
		otelhttp.WithTracerProvider(tp),
	))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	server := &http.Server{
		Addr:    port,
		Handler: mux,
	}

	accountingLogger = otelslog.NewLogger("accounting", otelslog.WithLoggerProvider(lp))
	accountingLogger.Info("Accounting Service starting", "port", port)
	return server
}

func handleAccountingConsume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get span from otelhttp handler (already creates "orders receive" span)
	span := trace.SpanFromContext(ctx)

	// Add Kafka messaging attributes to the existing span
	span.SetAttributes(
		attribute.String("messaging.system", "kafka"),
		attribute.String("messaging.destination.name", "orders"),
		attribute.String("messaging.operation.type", "receive"),
		attribute.String("messaging.consumer.group.name", "accountingservice"),
	)

	accountingLogger.InfoContext(ctx, "Received order from Kafka", "topic", "orders", "consumer_group", "accountingservice")

	// Simulate processing order for accounting
	processOrder(ctx)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "processed"})
}

func processOrder(ctx context.Context) {
	ctx, span := accountingTracer.Start(ctx, "processOrder")
	defer span.End()

	orderID := "order-" + randomString(8)
	amount := float64(common.WorkloadIntn(50000)+1000) / 100.0
	currency := []string{"USD", "EUR", "GBP", "JPY"}[common.WorkloadIntn(4)]

	accountingLogger.InfoContext(ctx, "ProcessOrder started", "order_id", orderID, "amount", amount, "currency", currency)

	span.SetAttributes(
		attribute.String("app.order.id", orderID),
		attribute.Float64("app.order.amount", amount),
		attribute.String("app.order.currency", currency),
	)

	ordersProcessed.Add(ctx, 1, metric.WithAttributes(
		attribute.String("currency", currency),
	))
	revenueTotal.Add(ctx, amount, metric.WithAttributes(
		attribute.String("currency", currency),
	))

	span.AddEvent("order_recorded", trace.WithAttributes(
		attribute.String("app.order.id", orderID),
	))

	accountingLogger.InfoContext(ctx, "Order processed for accounting",
		"order_id", orderID,
		"amount", amount,
		"currency", currency,
	)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
