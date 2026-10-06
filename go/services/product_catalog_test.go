package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type catalogLogExporter struct {
	records []sdklog.Record
}

func (e *catalogLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	for _, record := range records {
		e.records = append(e.records, record.Clone())
	}
	return nil
}

func (*catalogLogExporter) Shutdown(context.Context) error   { return nil }
func (*catalogLogExporter) ForceFlush(context.Context) error { return nil }

func newTestCatalog(t *testing.T, readers ...sdkmetric.Reader) (http.Handler, *tracetest.InMemoryExporter, *catalogLogExporter) {
	t.Helper()
	spans := tracetest.NewInMemoryExporter()
	res := resource.NewWithAttributes("", attribute.String("service.name", "product-catalog"))
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans), sdktrace.WithResource(res))
	logs := &catalogLogExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(logs)), sdklog.WithResource(res))
	options := []sdkmetric.Option{sdkmetric.WithResource(res)}
	for _, reader := range readers {
		options = append(options, sdkmetric.WithReader(reader))
	}
	mp := sdkmetric.NewMeterProvider(options...)
	server := InitProductCatalogServer(":0", tp, lp, mp)
	db := sqliteDB
	t.Cleanup(func() {
		db.Close()
		tp.Shutdown(context.Background())
		lp.Shutdown(context.Background())
		mp.Shutdown(context.Background())
	})
	return server.Handler, spans, logs
}

func TestProductCatalogLookup(t *testing.T) {
	handler, spans, logs := newTestCatalog(t)
	for _, tc := range []struct {
		name   string
		path   string
		status int
	}{
		{"liveness", "/health", http.StatusOK},
		{"known product", "/products/OLJCESPC7Z", http.StatusOK},
		{"unknown product", "/products/DOES-NOT-EXIST", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("GET %s = %d, want %d; body=%s", tc.path, w.Code, tc.status, w.Body.String())
			}
			if tc.name == "known product" {
				var product Product
				if err := json.Unmarshal(w.Body.Bytes(), &product); err != nil {
					t.Fatal(err)
				}
				if product.ID != "OLJCESPC7Z" || product.Name != "Sunglasses" || product.Price != 19.99 {
					t.Fatalf("unexpected product: %+v", product)
				}
			}
		})
	}
	if len(logs.records) != 2 {
		t.Fatalf("got %d lookup logs, want 2", len(logs.records))
	}
	miss := logs.records[1]
	if miss.Body().AsString() != "Product not found" || miss.SeverityText() != "WARN" {
		t.Fatalf("missing warning: %s %s", miss.SeverityText(), miss.Body().AsString())
	}
	if !miss.TraceID().IsValid() || !miss.SpanID().IsValid() {
		t.Fatal("catalog warning has no trace correlation")
	}
	for _, span := range spans.GetSpans() {
		if span.Name == "GetProduct" && span.SpanContext.SpanID() == miss.SpanID() {
			if span.SpanContext.TraceID() != miss.TraceID() || span.Status.Code != codes.Unset {
				// OTel HTTP server 404s do not imply a server fault.
				t.Fatalf("unexpected correlated 404 span: %+v", span)
			}
			return
		}
	}
	t.Fatal("warning does not correlate with a GetProduct span")
}

func TestProductCatalogDatabaseFailure(t *testing.T) {
	handler, spans, logs := newTestCatalog(t)
	if _, err := sqliteDB.Exec("DROP TABLE products"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/products/OLJCESPC7Z", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("database failure = %d, want 500", w.Code)
	}
	if len(logs.records) != 1 || logs.records[0].SeverityText() != "ERROR" {
		t.Fatal("database failure did not emit an ERROR log")
	}
	for _, span := range spans.GetSpans() {
		if span.Name == "GetProduct" && span.Status.Code == codes.Error {
			return
		}
	}
	t.Fatal("database failure did not mark the server span Error")
}

func TestProductCatalogExportsRequestMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	handler, _, _ := newTestCatalog(t, reader)
	for _, id := range []string{"OLJCESPC7Z", "OLJCESPC7Z", "DOES-NOT-EXIST"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/products/"+id, nil))
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	for _, scope := range data.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if measurement.Name != "app.products.requests" {
				continue
			}
			sum, ok := measurement.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("unexpected metric aggregation: %T", measurement.Data)
			}
			counts := map[string]int64{}
			for _, point := range sum.DataPoints {
				method, _ := point.Attributes.Value("method")
				status, _ := point.Attributes.Value("status")
				if method.AsString() == "GetProduct" {
					counts[status.AsString()] += point.Value
				}
				if _, present := point.Attributes.Value("app.product.id"); present {
					t.Fatal("SKU leaked into metric labels")
				}
			}
			if counts["found"] != 2 || counts["not_found"] != 1 {
				t.Fatalf("request metric counts = %v, want found=2 and not_found=1", counts)
			}
			return
		}
	}
	t.Fatal("app.products.requests was not emitted")
}
