package scenario

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func TestEndpointRestriction(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:4318/v1/metrics", "http://[::1]:4318/v1/metrics",
		"http://localhost:4318/v1/metrics", "http://otel-collector:4318/v1/metrics",
	} {
		if err := ValidateEndpoint(endpoint); err != nil {
			t.Errorf("rejected local endpoint %q: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"", "http://ingest.us.signoz.cloud:4318/v1/metrics", "http://10.0.0.1:4318/v1/metrics",
		"http://otel-collector.example.com:4318/v1/metrics", "https://127.0.0.1:4318/v1/metrics",
		"http://user:secret@127.0.0.1:4318/v1/metrics", "http://127.0.0.1/v1/metrics",
		"http://127.0.0.1:4318/v1/metrics?key=secret", "http://127.0.0.1:4318/v1/metrics#fragment",
		"http://127.0.0.1:4318/other", "http://127.0.0.1:bad/v1/metrics",
	} {
		if err := ValidateEndpoint(endpoint); err == nil {
			t.Errorf("accepted forbidden endpoint %q", endpoint)
		}
	}
}

func TestExportAcknowledgementFailuresAndNoRetries(t *testing.T) {
	partial, _ := proto.Marshal(&collectorpb.ExportMetricsServiceResponse{PartialSuccess: &collectorpb.ExportMetricsPartialSuccess{RejectedDataPoints: 1}})
	warning, _ := proto.Marshal(&collectorpb.ExportMetricsServiceResponse{PartialSuccess: &collectorpb.ExportMetricsPartialSuccess{ErrorMessage: "warning"}})
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		wantError               bool
	}{
		{"success", "application/x-protobuf", "", 200, false},
		{"partial", "application/x-protobuf", string(partial), 200, true},
		{"warning", "application/x-protobuf", string(warning), 200, true},
		{"invalid-body", "application/x-protobuf", "not protobuf", 200, true},
		{"wrong-content-type", "text/html", "", 200, true},
		{"too-large", "application/x-protobuf", strings.Repeat("x", maxResponseBytes+1), 200, true},
		{"unavailable", "application/x-protobuf", "", 503, true},
		{"redirect", "application/x-protobuf", "", 307, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v1/metrics" || r.Header.Get("Content-Type") != "application/x-protobuf" {
					t.Error("wrong OTLP request")
				}
				data, _ := io.ReadAll(r.Body)
				var parsed collectorpb.ExportMetricsServiceRequest
				if err := proto.Unmarshal(data, &parsed); err != nil || !proto.Equal(&parsed, fixtureForTest(t, ScopedCPU).Metrics) {
					t.Errorf("export changed metric payload: %v", err)
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Location", "http://example.invalid/v1/metrics")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			c := client()
			defer c.CloseIdleConnections()
			err := export(context.Background(), c, server.URL+"/v1/metrics", fixtureForTest(t, ScopedCPU).Metrics)
			if (err != nil) != tc.wantError || calls != 1 {
				t.Fatalf("error=%v, calls=%d", err, calls)
			}
		})
	}
}

func TestExecutionRequiresEvalModeBeforeAnyNetwork(t *testing.T) {
	t.Setenv("EVAL_MODE", "")
	if err := Execute(context.Background(), fixtureForTest(t, ScopedCPU), "http://127.0.0.1:1/v1/metrics"); err == nil || !strings.Contains(err.Error(), "EVAL_MODE=1") {
		t.Fatalf("execution did not stop at the authorization gate: %v", err)
	}
}

func TestFiniteExecutionAndReadinessFailure(t *testing.T) {
	t.Setenv("EVAL_MODE", "1")
	for _, failure := range []string{"", "health", "checkout", "checkout-body"} {
		t.Run(failure, func(t *testing.T) {
			var calls []string
			var traceParents []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				switch r.URL.Path {
				case "/health":
					if r.Header.Get("traceparent") != "" {
						t.Error("health check must not reuse checkout trace context")
					}
					if failure == "health" {
						w.WriteHeader(503)
					}
					_, _ = io.WriteString(w, `{"status":"ok"}`)
				case "/api/checkout":
					traceParents = append(traceParents, r.Header.Get("traceparent"))
					if r.Header.Get("baggage") != "" {
						t.Error("private metadata must not enter baggage")
					}
					if failure == "checkout" {
						w.WriteHeader(500)
					}
					if failure == "checkout-body" {
						_, _ = io.WriteString(w, `{"error":"downstream unavailable"}`)
					} else {
						_, _ = io.WriteString(w, `{"status":"order_placed"}`)
					}
				case "/v1/metrics":
					w.Header().Set("Content-Type", "application/x-protobuf")
				default:
					t.Error("unexpected request")
				}
			}))
			defer server.Close()
			f := fixtureForTest(t, ScopedCPU)
			for i := range f.Manifest.RequiredServices {
				f.Manifest.RequiredServices[i].HealthURL = server.URL + "/health"
			}
			for i := range f.Manifest.RequestPlan {
				f.Manifest.RequestPlan[i].URL = server.URL + "/api/checkout"
			}
			err := Execute(context.Background(), f, server.URL+"/v1/metrics")
			if (err != nil) != (failure != "") {
				t.Fatalf("unexpected execution outcome: %v", err)
			}
			want := make([]string, 13)
			for i := range want {
				want[i] = "GET /health"
			}
			switch failure {
			case "health":
				want = want[:1]
			case "checkout", "checkout-body":
				want = append(want, "POST /api/checkout")
			default:
				want = append(want, "POST /api/checkout", "POST /api/checkout", "POST /api/checkout", "POST /v1/metrics")
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("unexpected request order/count: %v", calls)
			}
			for i, traceParent := range traceParents {
				if traceParent != f.Manifest.RequestPlan[i].TraceParent || traceParent == "" {
					t.Fatal("request lost its ordinary correlation trace context")
				}
			}
		})
	}
}

func TestProxyDisabledAndBoundedClient(t *testing.T) {
	c := client()
	defer c.CloseIdleConnections()
	if c.Timeout <= 0 || c.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("client must be bounded and ignore inherited proxies")
	}
	if c.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("client must reject redirects")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := export(ctx, c, "http://127.0.0.1:1/v1/metrics", fixtureForTest(t, ScopedCPU).Metrics); err == nil {
		t.Fatal("cancelled context should fail")
	}
}
