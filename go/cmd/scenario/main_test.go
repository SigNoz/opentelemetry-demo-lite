package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"otel-mock/scenario"
	"sync/atomic"
	"testing"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func args() []string {
	return []string{"--world", scenario.ScopedCPU, "--run-id", "private-run-701", "--reference-time", "2026-09-16T12:00:00Z"}
}

func TestDefaultDryRunCannotSendRequests(t *testing.T) {
	t.Setenv("EVAL_MODE", "1")
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	var out, stderr bytes.Buffer
	if err := run(append(args(), "--endpoint", server.URL+"/v1/metrics"), &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var manifest scenario.Manifest
	if err := json.Unmarshal(out.Bytes(), &manifest); err != nil || manifest.SchemaVersion != 1 || len(manifest.Samples) != 9 {
		t.Fatalf("invalid private manifest output: %v", err)
	}
	if calls.Load() != 0 || stderr.Len() != 0 {
		t.Fatal("dry run must only write the private manifest to stdout")
	}
}

func TestAllOutputFormats(t *testing.T) {
	for _, format := range []string{"otlp-json", "otlp-proto"} {
		var out, stderr bytes.Buffer
		if err := run(append(args(), "--format", format), &out, &stderr); err != nil {
			t.Fatal(err)
		}
		var payload collectorpb.ExportMetricsServiceRequest
		var err error
		if format == "otlp-json" {
			err = protojson.Unmarshal(out.Bytes(), &payload)
		} else {
			err = proto.Unmarshal(out.Bytes(), &payload)
		}
		if err != nil || len(payload.ResourceMetrics) != 3 {
			t.Fatalf("invalid %s output: %v", format, err)
		}
	}
}

func TestInvalidArgumentsAndExecutionGate(t *testing.T) {
	t.Setenv("EVAL_MODE", "")
	for _, input := range [][]string{
		{}, {"--world", scenario.ScopedCPU}, append(args(), "--format", "unknown"),
		append(args(), "--execute"), append(args(), "surprise"),
		append(args(), "--reference-time", "2026-09-16T12:00:00.1Z"),
		append(args(), "--endpoint", "http://ingest.us.signoz.cloud:4318/v1/metrics"),
	} {
		var out, stderr bytes.Buffer
		if err := run(input, &out, &stderr); err == nil || out.Len() != 0 {
			t.Fatalf("invalid invocation produced output or succeeded: %v", input)
		}
	}
}
