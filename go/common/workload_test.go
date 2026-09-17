package common

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type shutdownExporter struct{ err error }

func (e shutdownExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (e shutdownExporter) Shutdown(context.Context) error                             { return e.err }

func TestTelemetryShutdownSurfacesExporterFailure(t *testing.T) {
	want := errors.New("flush failed")
	for _, failure := range []error{nil, want} {
		providers := &TelemetryProviders{TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSyncer(shutdownExporter{failure}))}
		if got := providers.Shutdown(context.Background()); !errors.Is(got, failure) {
			t.Fatalf("shutdown error=%v want=%v", got, failure)
		}
	}
	if err := (&TelemetryProviders{}).Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type batchFailureExporter struct {
	exportErr   error
	shutdownErr error
}

func (e batchFailureExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return e.exportErr
}

func (e batchFailureExporter) Shutdown(context.Context) error { return e.shutdownErr }

func TestWorkloadCapturesBatchProcessorFailures(t *testing.T) {
	t.Setenv("EVAL_MODE", "1")
	original := otel.GetErrorHandler()
	t.Cleanup(func() { otel.SetErrorHandler(original) })
	exportErr, shutdownErr := errors.New("queued export failed"), errors.New("exporter shutdown failed")
	for _, tc := range []struct {
		name     string
		exporter batchFailureExporter
		want     error
	}{
		{"queued-export", batchFailureExporter{exportErr: exportErr}, exportErr},
		{"shutdown", batchFailureExporter{shutdownErr: shutdownErr}, shutdownErr},
		{"first-error", batchFailureExporter{exportErr, shutdownErr}, exportErr},
		{"success", batchFailureExporter{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := CaptureWorkloadTelemetryErrors()
			provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(tc.exporter, sdktrace.WithBatchTimeout(time.Hour)))
			_, span := provider.Tracer("workload").Start(context.Background(), "checkout")
			span.End()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := (&TelemetryProviders{TracerProvider: provider}).Shutdown(ctx); err != nil {
				t.Fatalf("pinned batch processor unexpectedly returned its exporter failure: %v", err)
			}
			if got := observed.Err(); !errors.Is(got, tc.want) {
				t.Fatalf("captured error=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestWorkloadErrorCaptureIsBoundedConcurrentAndKeepsLogging(t *testing.T) {
	var output bytes.Buffer
	original := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(original) })
	observed := &WorkloadTelemetryErrors{}
	first, later := errors.New("first SDK failure"), errors.New("later SDK failure")
	observed.Handle(first)
	var workers sync.WaitGroup
	for range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			observed.Handle(later)
			_ = observed.Err()
		}()
	}
	workers.Wait()
	if observed.Err() != first {
		t.Fatal("later errors replaced the first failure")
	}
	if !strings.Contains(output.String(), first.Error()) || strings.Count(output.String(), later.Error()) != 20 {
		t.Fatal("SDK errors must still be logged")
	}
}

func TestNormalModeDoesNotReplaceSDKErrorHandler(t *testing.T) {
	t.Setenv("EVAL_MODE", "")
	original := otel.GetErrorHandler()
	observed := CaptureWorkloadTelemetryErrors()
	if otel.GetErrorHandler() != original || observed.Err() != nil {
		t.Fatal("normal mode changed the global SDK error handler")
	}
}

func TestWorkloadModeIsExplicit(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes"} {
		t.Setenv("EVAL_MODE", value)
		if EvalEnabled() {
			t.Fatalf("unexpected opt-in %q", value)
		}
		if n := WorkloadIntn(5); n < 0 || n >= 5 {
			t.Fatal(n)
		}
	}
	t.Setenv("EVAL_MODE", "1")
	for i := 0; i < 10; i++ {
		if WorkloadIntn(5) != 0 {
			t.Fatal("unstable workload input")
		}
	}
}

func TestWorkloadResourceDoesNotExposeProcess(t *testing.T) {
	t.Setenv("EVAL_MODE", "1")
	r := initResource("checkout")
	attrs := r.Set()
	for key, want := range map[string]string{"service.name": "checkout-api", "account": "northstar-retail", "deployment.environment": "prod", "cluster": "main"} {
		value, ok := attrs.Value(attribute.Key(key))
		if !ok || value.AsString() != want {
			t.Fatalf("%s = %v", key, value)
		}
	}
	if len(r.Attributes()) != 6 {
		t.Fatalf("unexpected resource metadata: %v", r.Attributes())
	}
}

func TestWorkloadHealthFailsClosedAndIsOptIn(t *testing.T) {
	for _, mode := range []string{"", "1"} {
		t.Setenv("EVAL_MODE", mode)
		for _, ready := range []bool{false, true} {
			mux := http.NewServeMux()
			AddWorkloadHealth(mux, func() bool { return ready })
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
			want := http.StatusNotFound
			if mode == "1" {
				want = http.StatusServiceUnavailable
				if ready {
					want = http.StatusOK
				}
			}
			if w.Code != want {
				t.Fatalf("mode %s ready %t: %d", mode, ready, w.Code)
			}
		}
	}
}
