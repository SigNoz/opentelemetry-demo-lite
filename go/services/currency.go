package services

import (
	"fmt"
	"log/slog"
	"net/http"
	"otel-mock/common"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var (
	currencyLogger  *slog.Logger
	currencyMeter   metric.Meter
	currencyCounter metric.Int64Counter
)

// Exchange rates from USD
var exchangeRates = map[string]float64{
	"USD": 1.0,
	"EUR": 0.85,
	"GBP": 0.73,
	"JPY": 110.0,
	"CAD": 1.25,
	"CHF": 0.92,
	"AUD": 1.35,
	"INR": 83.0,
}

func initCurrencyMetrics() {
	currencyMeter = otel.Meter("currency")
	var err error

	currencyCounter, err = currencyMeter.Int64Counter("app.currency_counter",
		metric.WithDescription("Currency conversion operations"),
		metric.WithUnit("{conversions}"))
	if err != nil {
		panic(err)
	}
}

func RunCurrencyService(tp trace.TracerProvider, lp otellog.LoggerProvider) {
	currencyLogger = otelslog.NewLogger("currency", otelslog.WithLoggerProvider(lp))
	initCurrencyMetrics()

	convertHandler := otelhttp.NewHandler(
		http.HandlerFunc(convertHandler),
		"Convert",
		otelhttp.WithTracerProvider(tp),
	)

	supportedHandler := otelhttp.NewHandler(
		http.HandlerFunc(getSupportedCurrenciesHandler),
		"GetSupportedCurrencies",
		otelhttp.WithTracerProvider(tp),
	)

	mux := http.NewServeMux()
	common.AddWorkloadHealth(mux, nil)
	mux.Handle("/convert", convertHandler)
	mux.Handle("/currencies", supportedHandler)

	port := ":8089"
	currencyLogger.Info("Currency Service starting", "port", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		currencyLogger.Error("Currency Service failed", "error", err)
	}
}

func convertHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	from := r.URL.Query().Get("from")
	if from == "" {
		from = "USD"
	}
	to := r.URL.Query().Get("to")
	if to == "" {
		to = "EUR"
	}

	// Set gRPC-style attributes (like C++ currency service)
	span.SetAttributes(
		attribute.String("app.currency.conversion.from", from),
		attribute.String("app.currency.conversion.to", to),
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.service", "oteldemo.CurrencyService"),
		attribute.String("rpc.method", "Convert"),
	)

	// Simulate conversion calculation
	fromRate, ok := exchangeRates[from]
	if !ok {
		fromRate = 1.0
	}
	toRate, ok := exchangeRates[to]
	if !ok {
		toRate = 1.0
	}

	rate := toRate / fromRate

	currencyCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("currency_code", to),
		attribute.String("from_currency", from),
	))

	currencyLogger.InfoContext(ctx, "Convert",
		"from", from,
		"to", to,
		"rate", rate,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"from": "%s", "to": "%s", "rate": %.4f}`, from, to, rate)
}

func getSupportedCurrenciesHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	span.SetAttributes(
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.service", "oteldemo.CurrencyService"),
		attribute.String("rpc.method", "GetSupportedCurrencies"),
		attribute.Int("app.currencies.count", len(exchangeRates)),
	)

	currencies := make([]string, 0, len(exchangeRates))
	for code := range exchangeRates {
		currencies = append(currencies, code)
	}

	currencyLogger.InfoContext(ctx, "GetSupportedCurrencies",
		"count", len(currencies),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"currencies": %d}`, len(currencies))
}
