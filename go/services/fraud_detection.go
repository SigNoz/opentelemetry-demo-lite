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
	fraudTracer trace.Tracer
	fraudMeter  metric.Meter
	fraudLogger *slog.Logger
)

var (
	ordersScanned  metric.Int64Counter
	fraudsDetected metric.Int64Counter
)

func InitFraudDetectionService(port string, tp trace.TracerProvider, mp metric.MeterProvider, lp otellog.LoggerProvider) *http.Server {
	fraudTracer = tp.Tracer("fraud-detection")
	fraudMeter = mp.Meter("fraud-detection")

	var err error
	ordersScanned, err = fraudMeter.Int64Counter("app.fraud.orders_scanned",
		metric.WithDescription("Total orders scanned for fraud"),
		metric.WithUnit("{orders}"))
	if err != nil {
		slog.Error("Failed to create orders_scanned counter", "error", err)
	}

	fraudsDetected, err = fraudMeter.Int64Counter("app.fraud.detected",
		metric.WithDescription("Total fraudulent orders detected"),
		metric.WithUnit("{orders}"))
	if err != nil {
		slog.Error("Failed to create frauds_detected counter", "error", err)
	}

	mux := http.NewServeMux()
	// Wrap with otelhttp to extract trace context from incoming requests
	mux.Handle("/consume", otelhttp.NewHandler(
		http.HandlerFunc(handleFraudConsume),
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

	fraudLogger = otelslog.NewLogger("fraud-detection", otelslog.WithLoggerProvider(lp))
	fraudLogger.Info("Fraud Detection Service starting", "port", port)
	return server
}

func handleFraudConsume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get span from otelhttp handler (already creates "orders receive" span)
	span := trace.SpanFromContext(ctx)

	// Add Kafka messaging attributes to the existing span
	span.SetAttributes(
		attribute.String("messaging.system", "kafka"),
		attribute.String("messaging.destination.name", "orders"),
		attribute.String("messaging.operation.type", "receive"),
		attribute.String("messaging.consumer.group.name", "frauddetectionservice"),
	)

	fraudLogger.InfoContext(ctx, "Received order from Kafka", "topic", "orders", "consumer_group", "frauddetectionservice")

	// Simulate fraud detection
	fraudDetected := detectFraud(ctx)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "scanned",
		"is_fraud": fraudDetected,
	})
}

func detectFraud(ctx context.Context) bool {
	ctx, span := fraudTracer.Start(ctx, "detectFraud")
	defer span.End()

	orderID := "order-" + randomString(8)
	amount := float64(common.WorkloadIntn(50000)+1000) / 100.0
	userID := "user-" + randomString(6)
	if common.EvalEnabled() {
		userID = "user-1042"
	}

	fraudLogger.InfoContext(ctx, "DetectFraud started", "order_id", orderID, "user_id", userID, "amount", amount)

	span.SetAttributes(
		attribute.String("app.order.id", orderID),
		attribute.Float64("app.order.amount", amount),
		attribute.String("app.user.id", userID),
	)

	// 2% chance of fraud detection
	isFraud := !common.EvalEnabled() && rand.Float32() < 0.02

	span.SetAttributes(attribute.Bool("app.fraud.detected", isFraud))

	ordersScanned.Add(ctx, 1)

	if isFraud {
		fraudsDetected.Add(ctx, 1)
		span.AddEvent("fraud_detected", trace.WithAttributes(
			attribute.String("app.order.id", orderID),
			attribute.String("app.fraud.reason", "suspicious_pattern"),
		))
		fraudLogger.WarnContext(ctx, "Fraud detected!",
			"order_id", orderID,
			"user_id", userID,
			"amount", amount,
		)
	} else {
		span.AddEvent("order_cleared")
		fraudLogger.InfoContext(ctx, "Order cleared",
			"order_id", orderID,
		)
	}

	return isFraud
}
