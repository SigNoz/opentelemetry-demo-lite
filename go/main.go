package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"otel-mock/common"
	"otel-mock/services"
)

func main() {
	service := flag.String("service", "all", "Service to run: all, checkout, shipping, product-catalog, cart, currency")
	flag.Parse()

	ctx := context.Background()
	if common.EvalEnabled() {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}

	switch *service {
	case "all":
		if err := runAllServices(ctx); err != nil {
			log.Printf("telemetry shutdown failed: %v", err)
			os.Exit(1)
		}
	default:
		log.Fatalf("Unknown service: %s", *service)
	}
}

func runAllServices(ctx context.Context) error {
	telemetryErrors := common.CaptureWorkloadTelemetryErrors()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var providers []*common.TelemetryProviders
	initTelemetry := func(name string) *common.TelemetryProviders {
		tel := common.InitTelemetry(ctx, name)
		mu.Lock()
		providers = append(providers, tel)
		mu.Unlock()
		return tel
	}

	// Start servers first
	wg.Add(1)
	go func() {
		defer wg.Done()
		tel := initTelemetry("shipping")
		defer tel.Shutdown(ctx)
		services.RunShippingService(tel.TracerProvider, tel.LoggerProvider)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		tel := initTelemetry("product-catalog")
		defer tel.Shutdown(ctx)
		services.RunProductCatalogService(tel.TracerProvider, tel.LoggerProvider)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		tel := initTelemetry("cart")
		defer tel.Shutdown(ctx)
		services.RunCartService(tel.TracerProvider, tel.LoggerProvider)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		tel := initTelemetry("currency")
		defer tel.Shutdown(ctx)
		services.RunCurrencyService(tel.TracerProvider, tel.LoggerProvider)
	}()

	// Kafka consumer services (accounting and fraud-detection)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tel := initTelemetry("accounting")
		defer tel.Shutdown(ctx)
		server := services.InitAccountingService(":8091", tel.TracerProvider, tel.MeterProvider, tel.LoggerProvider)
		server.ListenAndServe()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		tel := initTelemetry("fraud-detection")
		defer tel.Shutdown(ctx)
		server := services.InitFraudDetectionService(":8092", tel.TracerProvider, tel.MeterProvider, tel.LoggerProvider)
		server.ListenAndServe()
	}()

	// Checkout HTTP server
	wg.Add(1)
	go func() {
		defer wg.Done()
		tel := initTelemetry("checkout")
		defer tel.Shutdown(ctx)
		server := services.InitCheckoutServer(":8083", tel.TracerProvider, tel.LoggerProvider)
		server.ListenAndServe()
	}()

	// Wait for servers to start
	log.Println("Waiting for Go services to start...")
	time.Sleep(2 * time.Second)

	if common.EvalEnabled() {
		<-ctx.Done()
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		mu.Lock()
		readyProviders := append([]*common.TelemetryProviders(nil), providers...)
		mu.Unlock()
		results := make(chan error, len(readyProviders))
		for _, tel := range readyProviders {
			go func() { results <- tel.Shutdown(flush) }()
		}
		shutdownErr := waitForTelemetryShutdown(flush, results, len(readyProviders))
		return errors.Join(shutdownErr, telemetryErrors.Err())
	}
	wg.Wait()
	return nil
}

func waitForTelemetryShutdown(ctx context.Context, results <-chan error, count int) error {
	var failures []error
	for range count {
		select {
		case err := <-results:
			failures = append(failures, err)
		case <-ctx.Done():
			return errors.Join(append(failures, ctx.Err())...)
		}
	}
	return errors.Join(failures...)
}
