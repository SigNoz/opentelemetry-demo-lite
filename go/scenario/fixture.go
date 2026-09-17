// Package scenario constructs finite, private fixture plans without starting telemetry SDKs.
package scenario

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

const (
	CleanCheckout = "clean-checkout-v1"
	ScopedCPU     = "scoped-cpu-comparison-v1"
)

type Window struct {
	StartUnixNano string `json:"startUnixNano"`
	EndUnixNano   string `json:"endUnixNano"`
}

// Sample records both gauges at a single instant; it is not an exported label set.
type Sample struct {
	TimestampUnixNano string  `json:"timestampUnixNano"`
	Account           string  `json:"account"`
	Environment       string  `json:"environment"`
	Cluster           string  `json:"cluster"`
	Service           string  `json:"service"`
	Task              string  `json:"task"`
	Used              float64 `json:"used"`
	Reserved          float64 `json:"reserved"`
}

type Service struct {
	Name      string `json:"name"`
	HealthURL string `json:"healthUrl"`
}

type Request struct {
	Method      string `json:"method"`
	URL         string `json:"url"`
	Body        string `json:"body"`
	TraceParent string `json:"traceparent"`
}

type ExpectedFacts struct {
	CheckoutCount         int      `json:"checkoutCount"`
	CheckoutStatus        string   `json:"checkoutStatus"`
	RequestMethod         string   `json:"requestMethod"`
	RequestRoute          string   `json:"requestRoute"`
	HTTPStatus            int      `json:"httpStatus"`
	RequiredServiceNames  []string `json:"requiredServiceNames"`
	RequiredSpanNames     []string `json:"requiredSpanNames"`
	RequiredLogBodies     []string `json:"requiredLogBodies"`
	AllowedVariableFields []string `json:"allowedVariableFields"`
}

// Manifest is a private controller artifact. It must never become telemetry attributes.
type Manifest struct {
	SchemaVersion     int           `json:"schemaVersion"`
	ScenarioID        string        `json:"scenarioId"`
	RunID             string        `json:"runId"`
	ReferenceTime     string        `json:"referenceTime"`
	Window            Window        `json:"window"`
	Samples           []Sample      `json:"samples"`
	Controls          []Sample      `json:"controls"`
	RequiredServices  []Service     `json:"requiredServices"`
	RequestPlan       []Request     `json:"requestPlan"`
	ExpectedFacts     ExpectedFacts `json:"expectedFacts"`
	TelemetryVerified bool          `json:"telemetryVerified"`
}

type Fixture struct {
	Manifest Manifest
	Metrics  *collectorpb.ExportMetricsServiceRequest
}

var validRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Build performs no I/O. All instants derive from the one explicit controller clock.
func Build(world, runID string, reference time.Time) (*Fixture, error) {
	if world != CleanCheckout && world != ScopedCPU {
		return nil, fmt.Errorf("world must be %s or %s", CleanCheckout, ScopedCPU)
	}
	if !validRunID.MatchString(runID) {
		return nil, fmt.Errorf("run-id must contain 1-128 letters, digits, dots, underscores or hyphens and start with a letter or digit")
	}
	reference = reference.UTC()
	if reference.Nanosecond() != 0 || reference.Before(time.Unix(300, 0)) || reference.After(time.Unix(9223372036, 0)) {
		return nil, fmt.Errorf("reference-time must be a whole second between 1970-01-01T00:05:00Z and 2262-04-11T23:47:16Z")
	}
	end := reference.Add(-time.Minute)
	start := end.Add(-3 * time.Minute)
	m := Manifest{
		SchemaVersion: 1, ScenarioID: world, RunID: runID,
		ReferenceTime: reference.Format(time.RFC3339),
		Window:        Window{nano(start), nano(end)}, Samples: []Sample{}, Controls: []Sample{},
		RequiredServices: requiredServices(),
		ExpectedFacts: ExpectedFacts{
			CheckoutCount: 3, CheckoutStatus: "order_placed",
			RequestMethod: "POST", RequestRoute: "/api/checkout", HTTPStatus: 200,
			RequiredSpanNames:     []string{"POST /api/checkout", "PlaceOrder", "prepareOrderItemsAndShippingQuoteFromCart"},
			RequiredLogBodies:     []string{"Processing checkout request", "PlaceOrder started", "Order placed successfully"},
			AllowedVariableFields: []string{"trace_id", "span_id", "order_id", "transaction_id", "tracking_id", "message_id", "timestamps", "durations"},
		},
	}
	for _, service := range m.RequiredServices {
		m.ExpectedFacts.RequiredServiceNames = append(m.ExpectedFacts.RequiredServiceNames, service.Name)
	}
	for i := 0; i < 3; i++ {
		// These are ordinary W3C trace identifiers; private scenario/run names never travel
		// in baggage or request metadata. The private plan retains correlation for readback.
		id := sha256.Sum256([]byte(runID + "\x00request\x00" + strconv.Itoa(i)))
		m.RequestPlan = append(m.RequestPlan, Request{
			Method: "POST", URL: "http://127.0.0.1:8080/api/checkout", Body: "",
			TraceParent: fmt.Sprintf("00-%x-%x-01", id[:16], id[16:24]),
		})
	}
	if world == ScopedCPU {
		for task, values := range [][3]float64{{20, 80, 20}, {30, 30, 30}, {90, 90, 90}} {
			s := Sample{Account: "northstar-retail", Environment: "prod", Cluster: "main", Service: "checkout-api", Reserved: 100}
			s.Task = uuid.NewSHA1(uuid.NameSpaceOID, []byte(runID+"\x00"+strconv.Itoa(task))).String()
			if task == 1 {
				s.Reserved = 300
			}
			if task == 2 {
				s.Account, s.Environment = "northstar-sandbox", "dev"
			}
			for instant, used := range values {
				s.TimestampUnixNano = nano(start.Add(time.Duration(30+60*instant) * time.Second))
				s.Used = used
				m.Samples = append(m.Samples, s)
			}
			for _, at := range []time.Time{start.Add(-30 * time.Second), end.Add(30 * time.Second)} {
				s.TimestampUnixNano = nano(at)
				s.Used = []float64{95, 270, 10}[task]
				m.Controls = append(m.Controls, s)
			}
		}
	}
	return &Fixture{Manifest: m, Metrics: payload(m)}, nil
}

func nano(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

func requiredServices() []Service {
	var services []Service
	for _, s := range []struct {
		name string
		port int
	}{
		{"frontend", 8080}, {"payment", 8081}, {"shipping", 8082}, {"checkout-api", 8083},
		{"cart", 8084}, {"product-catalog", 8085}, {"recommendation", 8086}, {"ad", 8087},
		{"email", 8088}, {"currency", 8089}, {"accounting", 8091},
		{"fraud-detection", 8092}, {"quote", 8094},
	} {
		services = append(services, Service{s.name, fmt.Sprintf("http://127.0.0.1:%d/health", s.port)})
	}
	return services
}

func stringAttribute(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func payload(m Manifest) *collectorpb.ExportMetricsServiceRequest {
	request := &collectorpb.ExportMetricsServiceRequest{}
	// Resource attributes allow ordinary metric scope discovery. No process resource detector
	// or telemetry SDK is initialized, so private command arguments cannot be exported.
	for task := 0; task < len(m.Samples); task += 3 {
		s := m.Samples[task]
		used, reserved := &metricspb.Gauge{}, &metricspb.Gauge{}
		samples := append(append([]Sample{}, m.Samples[task:task+3]...), m.Controls[task/3*2:task/3*2+2]...)
		for _, point := range samples {
			at, _ := strconv.ParseUint(point.TimestampUnixNano, 10, 64)
			used.DataPoints = append(used.DataPoints, &metricspb.NumberDataPoint{
				TimeUnixNano: at, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: point.Used},
			})
			reserved.DataPoints = append(reserved.DataPoints, &metricspb.NumberDataPoint{
				TimeUnixNano: at, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: point.Reserved},
			})
		}
		request.ResourceMetrics = append(request.ResourceMetrics, &metricspb.ResourceMetrics{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				stringAttribute("account", s.Account), stringAttribute("deployment.environment", s.Environment),
				stringAttribute("cluster", s.Cluster), stringAttribute("service.name", s.Service), stringAttribute("task.id", s.Task),
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope: &commonpb.InstrumentationScope{Name: "workload.telemetry"},
				Metrics: []*metricspb.Metric{
					{Name: "app.workload.cpu.used", Description: "Application CPU capacity in use, in workload capacity units", Unit: "1", Data: &metricspb.Metric_Gauge{Gauge: used}},
					{Name: "app.workload.cpu.reserved", Description: "Application CPU capacity reserved, in workload capacity units", Unit: "1", Data: &metricspb.Metric_Gauge{Gauge: reserved}},
				},
			}},
		})
	}
	return request
}
