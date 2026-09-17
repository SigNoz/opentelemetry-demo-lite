package services

import (
	"context"
	"testing"

	lognoop "go.opentelemetry.io/otel/log/noop"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestFraudUserIsStableOnlyInWorkloadMode(t *testing.T) {
	for _, mode := range []string{"", "1"} {
		t.Setenv("EVAL_MODE", mode)
		exporter := tracetest.NewInMemoryExporter()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		InitFraudDetectionService(":0", provider, metricnoop.NewMeterProvider(), lognoop.NewLoggerProvider())
		for i := 0; i < 3; i++ {
			detectFraud(context.Background())
		}
		for _, span := range exporter.GetSpans() {
			found := false
			for _, attr := range span.Attributes {
				if attr.Key == "app.user.id" {
					found = true
					if (attr.Value.AsString() == "user-1042") != (mode == "1") {
						t.Fatalf("mode=%q user=%s", mode, attr.Value.AsString())
					}
				}
			}
			if !found {
				t.Fatal("missing user identity")
			}
		}
		if len(exporter.GetSpans()) != 3 {
			t.Fatal("missing fraud spans")
		}
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
