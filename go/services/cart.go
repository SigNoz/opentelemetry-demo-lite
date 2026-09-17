package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"otel-mock/common"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var (
	cartLogger     *slog.Logger
	cartMeter      metric.Meter
	addItemLatency metric.Float64Histogram
	getCartLatency metric.Float64Histogram
	cartOperations metric.Int64Counter
	redisClient    *redis.Client
)

type CartItem struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

func initCartMetrics() {
	cartMeter = otel.Meter("cart")
	var err error

	addItemLatency, err = cartMeter.Float64Histogram("app.cart.add_item.latency",
		metric.WithDescription("AddItem operation latency"),
		metric.WithUnit("ms"))
	if err != nil {
		panic(err)
	}

	getCartLatency, err = cartMeter.Float64Histogram("app.cart.get_cart.latency",
		metric.WithDescription("GetCart operation latency"),
		metric.WithUnit("ms"))
	if err != nil {
		panic(err)
	}

	cartOperations, err = cartMeter.Int64Counter("app.cart.operations",
		metric.WithDescription("Number of cart operations"),
		metric.WithUnit("{operations}"))
	if err != nil {
		panic(err)
	}
}

func initRedisClient(tp *sdktrace.TracerProvider) {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	redisClient = redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "",
		DB:       0,
	})

	if err := redisotel.InstrumentTracing(redisClient,
		redisotel.WithTracerProvider(tp),
		redisotel.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.name", "cart"),
		),
	); err != nil {
		log.Printf("Failed to instrument Redis: %v", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Printf("Warning: Redis not available at %s: %v", redisAddr, err)
	} else {
		log.Printf("Connected to Redis at %s", redisAddr)
	}
}

func RunCartService(tp *sdktrace.TracerProvider, lp otellog.LoggerProvider) {
	cartLogger = otelslog.NewLogger("cart", otelslog.WithLoggerProvider(lp))
	initCartMetrics()
	initRedisClient(tp)

	addHandler := otelhttp.NewHandler(
		http.HandlerFunc(addItemHandler),
		"AddItem",
		otelhttp.WithTracerProvider(tp),
	)

	getHandler := otelhttp.NewHandler(
		http.HandlerFunc(getCartHandler),
		"GetCart",
		otelhttp.WithTracerProvider(tp),
	)

	emptyHandler := otelhttp.NewHandler(
		http.HandlerFunc(emptyCartHandler),
		"EmptyCart",
		otelhttp.WithTracerProvider(tp),
	)

	mux := http.NewServeMux()
	common.AddWorkloadHealth(mux, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return redisClient != nil && redisClient.Ping(ctx).Err() == nil
	})
	mux.Handle("/cart/add", addHandler)
	mux.Handle("/cart", getHandler)
	mux.Handle("/cart/empty", emptyHandler)

	port := ":8084"
	cartLogger.Info("Cart Service starting", "port", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		cartLogger.Error("Cart Service failed", "error", err)
	}
}

func addItemHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = fmt.Sprintf("user-%d", common.WorkloadIntn(1000))
	}
	productID := r.URL.Query().Get("product_id")
	if productID == "" {
		productID = GetProductID()
	}
	quantity := common.WorkloadIntn(3) + 1

	span.SetAttributes(
		attribute.String("app.user.id", userID),
		attribute.String("app.product.id", productID),
		attribute.Int("app.product.quantity", quantity),
	)

	// Create cart item
	item := CartItem{ProductID: productID, Quantity: quantity}
	itemJSON, _ := json.Marshal(item)

	cartKey := fmt.Sprintf("cart:%s", userID)
	err := redisClient.HSet(ctx, cartKey, productID, itemJSON).Err()
	if err != nil {
		span.RecordError(err)
		cartLogger.ErrorContext(ctx, "Failed to add item to cart", "error", err)
		http.Error(w, "Failed to add item", http.StatusInternalServerError)
		return
	}

	redisClient.Expire(ctx, cartKey, time.Hour)

	duration := float64(time.Since(start).Milliseconds())
	addItemLatency.Record(ctx, duration)
	cartOperations.Add(ctx, 1, metric.WithAttributes(
		attribute.String("operation", "add_item"),
	))

	cartLogger.InfoContext(ctx, "AddItem",
		"user_id", userID,
		"product_id", productID,
		"quantity", quantity,
	)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status": "added", "user_id": "%s", "product_id": "%s"}`, userID, productID)
}

func getCartHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = fmt.Sprintf("user-%d", common.WorkloadIntn(1000))
	}

	span.SetAttributes(attribute.String("app.user.id", userID))
	span.AddEvent("Fetch cart")

	cartKey := fmt.Sprintf("cart:%s", userID)
	items, err := redisClient.HGetAll(ctx, cartKey).Result()
	if err != nil {
		span.RecordError(err)
		cartLogger.ErrorContext(ctx, "Failed to get cart", "error", err)
		http.Error(w, "Failed to get cart", http.StatusInternalServerError)
		return
	}

	totalItems := 0
	for _, itemJSON := range items {
		var item CartItem
		if json.Unmarshal([]byte(itemJSON), &item) == nil {
			totalItems += item.Quantity
		}
	}

	span.SetAttributes(attribute.Int("app.cart.items.count", totalItems))

	duration := float64(time.Since(start).Milliseconds())
	getCartLatency.Record(ctx, duration)
	cartOperations.Add(ctx, 1, metric.WithAttributes(
		attribute.String("operation", "get_cart"),
	))

	cartLogger.InfoContext(ctx, "GetCart",
		"user_id", userID,
		"items_count", totalItems,
	)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"user_id": "%s", "items_count": %d}`, userID, totalItems)
}

func emptyCartHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = fmt.Sprintf("user-%d", common.WorkloadIntn(1000))
	}

	span.SetAttributes(attribute.String("app.user.id", userID))
	span.AddEvent("Empty cart")

	cartKey := fmt.Sprintf("cart:%s", userID)
	err := redisClient.Del(ctx, cartKey).Err()
	if err != nil {
		span.RecordError(err)
		cartLogger.ErrorContext(ctx, "Failed to empty cart", "error", err)
		http.Error(w, "Failed to empty cart", http.StatusInternalServerError)
		return
	}

	cartOperations.Add(ctx, 1, metric.WithAttributes(
		attribute.String("operation", "empty_cart"),
	))

	cartLogger.InfoContext(ctx, "EmptyCart", "user_id", userID)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status": "emptied", "user_id": "%s"}`, userID)
}
