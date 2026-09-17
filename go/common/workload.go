package common

import (
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync"

	"go.opentelemetry.io/otel"
)

func EvalEnabled() bool { return os.Getenv("EVAL_MODE") == "1" }

// WorkloadTelemetryErrors retains only the first asynchronous SDK failure.
type WorkloadTelemetryErrors struct {
	mu    sync.Mutex
	first error
}

// CaptureWorkloadTelemetryErrors is called once, before workload SDK initialization.
func CaptureWorkloadTelemetryErrors() *WorkloadTelemetryErrors {
	observed := &WorkloadTelemetryErrors{}
	if EvalEnabled() {
		otel.SetErrorHandler(observed)
	}
	return observed
}

func (e *WorkloadTelemetryErrors) Handle(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	if e.first == nil {
		e.first = err
	}
	e.mu.Unlock()
	// The default SDK handler delegates to its replacement. Calling it here would
	// recurse; log.Print preserves its existing stderr logging behavior directly.
	log.Print(err)
}

func (e *WorkloadTelemetryErrors) Err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.first
}

// WorkloadIntn fixes semantic inputs without freezing runtime IDs or clocks.
func WorkloadIntn(n int) int {
	if EvalEnabled() {
		if n <= 0 {
			panic("invalid workload bound")
		}
		return 0
	}
	return rand.Intn(n)
}

func AddWorkloadHealth(mux *http.ServeMux, ready func() bool) {
	if !EvalEnabled() {
		return
	}
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if ready != nil && !ready() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}
