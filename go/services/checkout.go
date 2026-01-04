package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"otel-mock/config"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var (
	checkoutTracer  trace.Tracer
	checkoutLogger  *slog.Logger
	checkoutMeter   metric.Meter
	ordersCounter   metric.Int64Counter
	checkoutLatency metric.Float64Histogram
)

func initCheckoutMetrics() {
	checkoutMeter = otel.Meter("checkout")
	var err error
	ordersCounter, err = checkoutMeter.Int64Counter("app.checkout.orders_total",
		metric.WithDescription("Total number of orders placed"),
		metric.WithUnit("{orders}"))
	if err != nil {
		panic(err)
	}

	checkoutLatency, err = checkoutMeter.Float64Histogram("app.checkout.latency",
		metric.WithDescription("Checkout operation latency"),
		metric.WithUnit("ms"))
	if err != nil {
		panic(err)
	}
}

// InitCheckoutServer creates an HTTP server for checkout (receives requests from frontend)
func InitCheckoutServer(port string, tp trace.TracerProvider, lp otellog.LoggerProvider) *http.Server {
	checkoutLogger = otelslog.NewLogger("checkout", otelslog.WithLoggerProvider(lp))
	checkoutTracer = tp.Tracer("checkout")
	initCheckoutMetrics()

	// HTTP client for calling downstream services
	httpClient := &http.Client{
		Transport: otelhttp.NewTransport(
			http.DefaultTransport,
			otelhttp.WithTracerProvider(tp),
		),
	}

	handler := otelhttp.NewHandler(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			placeOrder(r.Context(), httpClient)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status": "order_placed"}`)
		}),
		"PlaceOrder",
		otelhttp.WithTracerProvider(tp),
	)

	mux := http.NewServeMux()
	mux.Handle("/checkout", handler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	server := &http.Server{
		Addr:    port,
		Handler: mux,
	}

	checkoutLogger.Info("Checkout HTTP Server starting", "port", port)
	return server
}

func placeOrder(ctx context.Context, client *http.Client) {
	start := time.Now()

	// Get the span from context (created by otelhttp handler or create new one for batch mode)
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		// Batch mode - create our own span
		var newSpan trace.Span
		ctx, newSpan = checkoutTracer.Start(ctx, "PlaceOrder", trace.WithSpanKind(trace.SpanKindServer))
		span = newSpan
		defer span.End()
	}

	userID := fmt.Sprintf("user-%d", rand.Intn(10000))
	currency := randomCurrency()
	orderID := uuid.New().String()

	// Set main span attributes (like real checkout service)
	span.SetAttributes(
		attribute.String("app.user.id", userID),
		attribute.String("app.user.currency", currency),
	)

	// Check for synthetic request baggage
	bag := baggage.FromContext(ctx)
	if m := bag.Member("synthetic_request"); m.Value() == "true" {
		span.SetAttributes(attribute.Bool("app.synthetic", true))
	}
	if m := bag.Member("session.id"); m.Value() != "" {
		span.SetAttributes(attribute.String("session.id", m.Value()))
	}

	checkoutLogger.InfoContext(ctx, "PlaceOrder started", "user_id", userID, "currency", currency)

	// Step 1: Prepare order items (calls cart service with Redis)
	prep, err := prepareOrderItems(ctx, client, userID, currency)
	if err != nil {
		span.RecordError(err)
		checkoutLogger.ErrorContext(ctx, "Prepare failed", "error", err)
		return
	}
	span.AddEvent("prepared", trace.WithAttributes(
		attribute.Int("app.order.items.count", prep.itemCount),
	))

	// Step 1b: Get product details from product-catalog
	getProductDetails(ctx, client, prep.productIDs)
	span.AddEvent("product_details_fetched")

	// Step 1c: Convert currency
	getCurrencyConversion(ctx, client, currency, prep.total)
	span.AddEvent("currency_converted")

	// Step 1d: Get recommendations (like real demo)
	getRecommendations(ctx, client, userID, prep.productIDs)
	span.AddEvent("recommendations_fetched")

	// Step 1e: Get ads (like real demo)
	getAds(ctx, client)
	span.AddEvent("ads_fetched")

	// Step 2: Charge payment
	txID, err := chargeCard(ctx, client, prep.total, currency)
	if err != nil {
		span.RecordError(err)
		checkoutLogger.ErrorContext(ctx, "Payment failed", "error", err)
		return
	}
	span.AddEvent("charged", trace.WithAttributes(
		attribute.String("app.payment.transaction.id", txID),
	))

	// Step 3: Ship order
	trackingID, err := shipOrder(ctx, client, prep.itemCount)
	if err != nil {
		span.RecordError(err)
		checkoutLogger.ErrorContext(ctx, "Shipping failed", "error", err)
		return
	}
	span.AddEvent("shipped", trace.WithAttributes(
		attribute.String("app.shipping.tracking.id", trackingID),
	))

	// Step 4: Send confirmation email
	err = sendOrderConfirmation(ctx, client, orderID, userID)
	if err != nil {
		checkoutLogger.WarnContext(ctx, "Email failed", "error", err)
	}
	span.AddEvent("email_sent")

	// Step 5: Mock Kafka publish (orders topic)
	publishToKafka(ctx, client, orderID)
	span.AddEvent("published_to_kafka", trace.WithAttributes(
		attribute.String("messaging.destination.name", "orders"),
	))

	// Final attributes
	span.SetAttributes(
		attribute.String("app.order.id", orderID),
		attribute.Float64("app.order.amount", prep.total),
		attribute.Float64("app.shipping.amount", prep.shippingCost),
		attribute.Int("app.order.items.count", prep.itemCount),
		attribute.String("app.shipping.tracking.id", trackingID),
	)

	// Record metrics
	duration := float64(time.Since(start).Milliseconds())
	ordersCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("currency", currency),
		attribute.String("status", "success"),
	))
	checkoutLatency.Record(ctx, duration, metric.WithAttributes(
		attribute.String("currency", currency),
	))

	checkoutLogger.InfoContext(ctx, "Order placed successfully",
		"order_id", orderID,
		"transaction_id", txID,
		"tracking_id", trackingID,
		"duration_ms", duration,
	)
}

type orderPrep struct {
	itemCount    int
	total        float64
	shippingCost float64
	productIDs   []string
}

func prepareOrderItems(ctx context.Context, client *http.Client, userID, currency string) (*orderPrep, error) {
	ctx, span := checkoutTracer.Start(ctx, "prepareOrderItemsAndShippingQuoteFromCart")
	defer span.End()

	checkoutLogger.InfoContext(ctx, "PrepareOrderItems started", "user_id", userID, "currency", currency)

	span.SetAttributes(
		attribute.String("app.user.id", userID),
		attribute.String("app.user.currency", currency),
	)

	// Step 1: Add items to cart (calls Redis via cart service)
	// Fixed item count for consistent trace depth
	itemCount := 3
	productIDs := make([]string, 0, itemCount)
	for i := 0; i < itemCount; i++ {
		productID := GetProductID()
		productIDs = append(productIDs, productID)
		if err := addToCart(ctx, client, userID, productID); err != nil {
			checkoutLogger.WarnContext(ctx, "Failed to add item to cart", "error", err)
		}
	}
	span.AddEvent("items_added_to_cart", trace.WithAttributes(
		attribute.Int("app.cart.items.count", itemCount),
	))

	// Step 2: Get cart contents (calls Redis via cart service)
	cartItems, err := getCart(ctx, client, userID)
	if err != nil {
		checkoutLogger.WarnContext(ctx, "Failed to get cart", "error", err)
	}
	span.AddEvent("cart_retrieved", trace.WithAttributes(
		attribute.String("app.user.id", userID),
		attribute.Int("app.cart.items.count", cartItems),
	))

	total := float64(rand.Intn(50000)+1000) / 100.0
	shippingCost := float64(rand.Intn(1000)+100) / 100.0

	// Step 3: Empty cart after checkout (calls Redis via cart service)
	if err := emptyCart(ctx, client, userID); err != nil {
		checkoutLogger.WarnContext(ctx, "Failed to empty cart", "error", err)
	}
	span.AddEvent("cart_emptied")

	return &orderPrep{
		itemCount:    itemCount,
		total:        total,
		shippingCost: shippingCost,
		productIDs:   productIDs,
	}, nil
}

func addToCart(ctx context.Context, client *http.Client, userID, productID string) error {
	checkoutLogger.InfoContext(ctx, "AddItem", "user_id", userID, "product_id", productID)
	url := fmt.Sprintf("%s/cart/add?user_id=%s&product_id=%s", config.CartURL, userID, productID)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		checkoutLogger.ErrorContext(ctx, "AddItem failed", "error", err)
		return err
	}
	defer resp.Body.Close()
	return nil
}

func getCart(ctx context.Context, client *http.Client, userID string) (int, error) {
	checkoutLogger.InfoContext(ctx, "GetCart", "user_id", userID)
	url := fmt.Sprintf("%s/cart?user_id=%s", config.CartURL, userID)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		checkoutLogger.ErrorContext(ctx, "GetCart failed", "error", err)
		return 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var res struct {
		ItemsCount int `json:"items_count"`
	}
	json.Unmarshal(body, &res)
	checkoutLogger.InfoContext(ctx, "GetCart result", "items_count", res.ItemsCount)
	return res.ItemsCount, nil
}

func emptyCart(ctx context.Context, client *http.Client, userID string) error {
	checkoutLogger.InfoContext(ctx, "EmptyCart", "user_id", userID)
	url := fmt.Sprintf("%s/cart/empty?user_id=%s", config.CartURL, userID)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		checkoutLogger.ErrorContext(ctx, "EmptyCart failed", "error", err)
		return err
	}
	defer resp.Body.Close()
	return nil
}

func chargeCard(ctx context.Context, client *http.Client, amount float64, currency string) (string, error) {
	ctx, span := checkoutTracer.Start(ctx, "chargeCard", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()

	checkoutLogger.InfoContext(ctx, "ChargeCard", "amount", amount, "currency", currency)

	span.SetAttributes(
		attribute.String("saga.step", "payment"),
		attribute.Float64("payment.amount", amount),
		attribute.String("payment.currency", currency),
	)

	req, _ := http.NewRequestWithContext(ctx, "POST", config.PaymentURL+"/charge", nil)
	resp, err := client.Do(req)
	if err != nil {
		checkoutLogger.ErrorContext(ctx, "ChargeCard failed", "error", err)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("payment service returned %d", resp.StatusCode)
		checkoutLogger.ErrorContext(ctx, "ChargeCard failed", "error", err)
		return "", err
	}

	body, _ := io.ReadAll(resp.Body)
	var res struct {
		TransactionID string `json:"transaction_id"`
	}
	json.Unmarshal(body, &res)

	checkoutLogger.InfoContext(ctx, "ChargeCard success", "transaction_id", res.TransactionID)
	span.SetAttributes(attribute.String("payment.transaction.id", res.TransactionID))
	return res.TransactionID, nil
}

func shipOrder(ctx context.Context, client *http.Client, itemCount int) (string, error) {
	ctx, span := checkoutTracer.Start(ctx, "shipOrder", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()

	checkoutLogger.InfoContext(ctx, "ShipOrder", "items", itemCount)

	span.SetAttributes(
		attribute.String("saga.step", "shipping"),
		attribute.Int("shipping.items.count", itemCount),
	)

	req, _ := http.NewRequestWithContext(ctx, "POST", config.ShippingURL+"/ship", nil)
	resp, err := client.Do(req)
	if err != nil {
		checkoutLogger.ErrorContext(ctx, "ShipOrder failed", "error", err)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("shipping service returned %d", resp.StatusCode)
		checkoutLogger.ErrorContext(ctx, "ShipOrder failed", "error", err)
		return "", err
	}

	body, _ := io.ReadAll(resp.Body)
	var res struct {
		TrackingID string `json:"tracking_id"`
	}
	json.Unmarshal(body, &res)

	checkoutLogger.InfoContext(ctx, "ShipOrder success", "tracking_id", res.TrackingID)
	span.SetAttributes(attribute.String("shipping.tracking.id", res.TrackingID))
	return res.TrackingID, nil
}

func sendOrderConfirmation(ctx context.Context, client *http.Client, orderID, userID string) error {
	ctx, span := checkoutTracer.Start(ctx, "sendOrderConfirmation", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()

	checkoutLogger.InfoContext(ctx, "SendOrderConfirmation", "order_id", orderID, "user_id", userID)

	span.SetAttributes(
		attribute.String("saga.step", "email"),
		attribute.String("app.order.id", orderID),
		attribute.String("app.user.id", userID),
	)

	req, _ := http.NewRequestWithContext(ctx, "POST", config.EmailURL+"/send", nil)
	resp, err := client.Do(req)
	if err != nil {
		checkoutLogger.ErrorContext(ctx, "SendOrderConfirmation failed", "error", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("email service returned %d", resp.StatusCode)
		checkoutLogger.ErrorContext(ctx, "SendOrderConfirmation failed", "error", err)
		return err
	}

	checkoutLogger.InfoContext(ctx, "SendOrderConfirmation success")
	return nil
}

func publishToKafka(ctx context.Context, client *http.Client, orderID string) {
	ctx, span := checkoutTracer.Start(ctx, "orders publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination.name", "orders"),
			attribute.String("messaging.operation.type", "publish"),
			attribute.String("messaging.kafka.destination.partition", "0"),
			attribute.String("app.order.id", orderID),
		))
	defer span.End()

	checkoutLogger.InfoContext(ctx, "PublishToKafka", "order_id", orderID, "topic", "orders")

	time.Sleep(time.Duration(rand.Intn(10)+5) * time.Millisecond)

	req, _ := http.NewRequestWithContext(ctx, "POST", config.AccountingURL+"/consume", nil)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}

	req, _ = http.NewRequestWithContext(ctx, "POST", config.FraudDetectionURL+"/consume", nil)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func randomCurrency() string {
	currencies := []string{"USD", "EUR", "GBP", "JPY", "CAD"}
	return currencies[rand.Intn(len(currencies))]
}

func getProductDetails(ctx context.Context, client *http.Client, productIDs []string) {
	ctx, span := checkoutTracer.Start(ctx, "getProductDetails",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	checkoutLogger.InfoContext(ctx, "GetProductDetails", "product_count", len(productIDs))

	span.SetAttributes(
		attribute.Int("app.products.count", len(productIDs)),
		attribute.StringSlice("app.product.ids", productIDs),
	)

	for _, productID := range productIDs {
		checkoutLogger.InfoContext(ctx, "FetchProduct", "product_id", productID)
		url := fmt.Sprintf("%s/products/%s", config.ProductCatalogURL, productID)
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := client.Do(req)
		if err != nil {
			checkoutLogger.WarnContext(ctx, "FetchProduct failed", "product_id", productID, "error", err)
			continue
		}
		resp.Body.Close()
	}
}

func getCurrencyConversion(ctx context.Context, client *http.Client, currency string, amount float64) {
	ctx, span := checkoutTracer.Start(ctx, "getCurrencyConversion",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	checkoutLogger.InfoContext(ctx, "GetCurrencyConversion", "from", "USD", "to", currency, "amount", amount)

	span.SetAttributes(
		attribute.String("app.currency.from", "USD"),
		attribute.String("app.currency.to", currency),
		attribute.Float64("app.currency.amount", amount),
	)

	url := fmt.Sprintf("%s/convert?from=USD&to=%s&amount=%.2f", config.CurrencyURL, currency, amount)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		checkoutLogger.WarnContext(ctx, "GetCurrencyConversion failed", "currency", currency, "error", err)
		return
	}
	resp.Body.Close()
}

func getRecommendations(ctx context.Context, client *http.Client, userID string, productIDs []string) {
	ctx, span := checkoutTracer.Start(ctx, "getRecommendations",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	checkoutLogger.InfoContext(ctx, "GetRecommendations", "user_id", userID)

	span.SetAttributes(
		attribute.String("app.user.id", userID),
		attribute.StringSlice("app.product.ids", productIDs),
	)

	url := fmt.Sprintf("%s/recommendations?user_id=%s", config.RecommendationURL, userID)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		checkoutLogger.WarnContext(ctx, "GetRecommendations failed", "error", err)
		return
	}
	resp.Body.Close()
}

func getAds(ctx context.Context, client *http.Client) {
	ctx, span := checkoutTracer.Start(ctx, "getAds",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	categories := []string{"clothing", "electronics", "home", "outdoor"}
	category := categories[rand.Intn(len(categories))]
	checkoutLogger.InfoContext(ctx, "GetAds", "category", category)

	span.SetAttributes(attribute.String("app.ads.category", category))

	url := fmt.Sprintf("%s/ads?category=%s", config.AdURL, category)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		checkoutLogger.WarnContext(ctx, "GetAds failed", "error", err)
		return
	}
	resp.Body.Close()
}
