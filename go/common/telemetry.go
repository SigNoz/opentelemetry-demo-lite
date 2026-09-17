package common

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v3/load"
	"go.opentelemetry.io/contrib/instrumentation/host"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

const serviceVersion = "1.0.0"

// TelemetryProviders holds all OTel providers for a service
type TelemetryProviders struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
	Tracer         trace.Tracer
}

// InitTelemetry initializes all OTel providers for a service
func InitTelemetry(ctx context.Context, serviceName string) *TelemetryProviders {
	res := initResource(serviceName)

	tp := initTracerProvider(ctx, res)
	mp := initMeterProvider(ctx, res)
	lp := initLoggerProvider(ctx, res)

	if !EvalEnabled() {
		if err := otelruntime.Start(otelruntime.WithMinimumReadMemStatsInterval(time.Second * 5)); err != nil {
			log.Printf("failed to start runtime metrics: %v", err)
		}
		if err := host.Start(host.WithMeterProvider(mp)); err != nil {
			log.Printf("failed to start host metrics: %v", err)
		}
		startHostMetrics(mp)
	}

	// Set global propagator for context propagation
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &TelemetryProviders{
		TracerProvider: tp,
		MeterProvider:  mp,
		LoggerProvider: lp,
		Tracer:         tp.Tracer(serviceName),
	}
}

func initResource(serviceName string) *sdkresource.Resource {
	if EvalEnabled() {
		if serviceName == "checkout" {
			serviceName = "checkout-api"
		}
		// Process detection exposes command arguments; private control data stays local.
		return sdkresource.NewSchemaless(
			semconv.ServiceName(serviceName), semconv.ServiceVersion(serviceVersion),
			semconv.TelemetrySDKLanguageGo,
			attribute.String("account", "northstar-retail"),
			attribute.String("deployment.environment", "prod"),
			attribute.String("cluster", "main"),
		)
	}
	hostName := fmt.Sprintf("%s-host", serviceName)

	res, err := sdkresource.New(
		context.Background(),
		sdkresource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
			semconv.TelemetrySDKLanguageGo,
			semconv.HostName(hostName),
			attribute.String("os.type", runtime.GOOS),
			attribute.String("deployment.environment", "demo"),
			attribute.String("container.runtime", "docker"),
		),
		sdkresource.WithProcess(),
		sdkresource.WithContainer(),
	)
	if err != nil {
		log.Fatalf("failed to create resource: %v", err)
	}
	return res
}

func initTracerProvider(ctx context.Context, res *sdkresource.Resource) *sdktrace.TracerProvider {
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		log.Fatalf("failed to create trace exporter: %v", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	return tp
}

func initMeterProvider(ctx context.Context, res *sdkresource.Resource) *sdkmetric.MeterProvider {
	if EvalEnabled() {
		// Only the finite scenario command exports the declared metric grid.
		return sdkmetric.NewMeterProvider(sdkmetric.WithResource(res))
	}
	exporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithInsecure())
	if err != nil {
		log.Fatalf("failed to create metric exporter: %v", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
		sdkmetric.WithResource(res),
	)
	return mp
}

func initLoggerProvider(ctx context.Context, res *sdkresource.Resource) *sdklog.LoggerProvider {
	exporter, err := otlploggrpc.New(ctx, otlploggrpc.WithInsecure())
	if err != nil {
		log.Fatalf("failed to create log exporter: %v", err)
	}

	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(res),
	)
	return lp
}

// Shutdown gracefully shuts down all providers
func (t *TelemetryProviders) Shutdown(ctx context.Context) error {
	var failures []error
	if t.TracerProvider != nil {
		failures = append(failures, t.TracerProvider.Shutdown(ctx))
	}
	if t.MeterProvider != nil {
		failures = append(failures, t.MeterProvider.Shutdown(ctx))
	}
	if t.LoggerProvider != nil {
		failures = append(failures, t.LoggerProvider.Shutdown(ctx))
	}
	return errors.Join(failures...)
}

func startHostMetrics(mp *sdkmetric.MeterProvider) {
	meter := mp.Meter("host-metrics")

	loadAvg15m, _ := meter.Float64ObservableGauge("system.cpu.load_average.15m",
		metric.WithDescription("15-minute CPU load average"), metric.WithUnit("1"))
	loadAvg1m, _ := meter.Float64ObservableGauge("system.cpu.load_average.1m",
		metric.WithDescription("1-minute CPU load average"), metric.WithUnit("1"))
	loadAvg5m, _ := meter.Float64ObservableGauge("system.cpu.load_average.5m",
		metric.WithDescription("5-minute CPU load average"), metric.WithUnit("1"))

	// Register callback for load averages
	_, err := meter.RegisterCallback(
		func(ctx context.Context, observer metric.Observer) error {
			if loadAvg, err := load.Avg(); err == nil {
				observer.ObserveFloat64(loadAvg1m, loadAvg.Load1)
				observer.ObserveFloat64(loadAvg5m, loadAvg.Load5)
				observer.ObserveFloat64(loadAvg15m, loadAvg.Load15)
			}
			return nil
		},
		loadAvg1m, loadAvg5m, loadAvg15m,
	)
	if err != nil {
		log.Printf("failed to register host metrics callback: %v", err)
	}
}
