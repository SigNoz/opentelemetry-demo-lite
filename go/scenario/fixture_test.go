package scenario

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func fixtureForTest(t *testing.T, world string) *Fixture {
	t.Helper()
	f, err := Build(world, "private-run-701", time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestExactCPUGridAndBoundaryControls(t *testing.T) {
	f := fixtureForTest(t, ScopedCPU)
	m := f.Manifest
	if m.SchemaVersion != 1 || m.ScenarioID != ScopedCPU || len(m.Samples) != 9 || len(m.Controls) != 6 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	start, _ := strconv.ParseUint(m.Window.StartUnixNano, 10, 64)
	end, _ := strconv.ParseUint(m.Window.EndUnixNano, 10, 64)
	reference, _ := time.Parse(time.RFC3339, m.ReferenceTime)
	if end-start != uint64(3*time.Minute) || uint64(reference.UnixNano())-end != uint64(time.Minute) {
		t.Fatal("window is not derived from reference time")
	}
	wantValues := [][]float64{{20, 80, 20}, {30, 30, 30}, {90, 90, 90}}
	wantReserved := []float64{100, 300, 100}
	var core, controls int
	for task, resource := range f.Metrics.ResourceMetrics {
		attrs := map[string]string{}
		for _, a := range resource.Resource.Attributes {
			attrs[a.Key] = a.Value.GetStringValue()
		}
		account, environment := "northstar-retail", "prod"
		if task == 2 {
			account, environment = "northstar-sandbox", "dev"
		}
		if len(attrs) != 5 || attrs["account"] != account || attrs["deployment.environment"] != environment || attrs["cluster"] != "main" || attrs["service.name"] != "checkout-api" {
			t.Fatalf("incorrect resource identity: %v", attrs)
		}
		if _, err := uuid.Parse(attrs["task.id"]); err != nil {
			t.Fatalf("task identity is not an ordinary UUID: %v", err)
		}
		scope := resource.ScopeMetrics[0]
		if scope.Scope.Name != "workload.telemetry" || len(scope.Scope.Attributes) != 0 || len(scope.Metrics) != 2 {
			t.Fatalf("unexpected instrumentation scope: %v", scope)
		}
		for metricIndex, metric := range scope.Metrics {
			if metric.Name != []string{"app.workload.cpu.used", "app.workload.cpu.reserved"}[metricIndex] || metric.Unit != "1" || !strings.Contains(metric.Description, "capacity") {
				t.Fatalf("incorrect metric metadata: %v", metric)
			}
			if len(metric.GetGauge().DataPoints) != 5 {
				t.Fatal("expected exactly three core and two control points per gauge")
			}
			for i, point := range metric.GetGauge().DataPoints {
				if len(point.Attributes) != 0 || point.StartTimeUnixNano != 0 || len(point.Exemplars) != 0 || point.Flags != 0 {
					t.Fatalf("unexpected extra point metadata: %v", point)
				}
				wantUsed := float64(0)
				var wantAt uint64
				if i < 3 {
					wantAt = start + uint64(time.Duration(30+60*i)*time.Second)
					wantUsed = wantValues[task][i]
					core++
				} else {
					wantAt = []uint64{start - uint64(30*time.Second), end + uint64(30*time.Second)}[i-3]
					wantUsed = []float64{95, 270, 10}[task]
					controls++
				}
				want := wantUsed
				if metricIndex == 1 {
					want = wantReserved[task]
				}
				if point.TimeUnixNano != wantAt || point.GetAsDouble() != want || point.TimeUnixNano >= uint64(reference.UnixNano()) {
					t.Fatalf("incorrect point %d for task %d: %v", i, task, point)
				}
				var sample Sample
				if i < 3 {
					sample = m.Samples[task*3+i]
				} else {
					sample = m.Controls[task*2+i-3]
				}
				if sample.TimestampUnixNano != strconv.FormatUint(wantAt, 10) || sample.Task != attrs["task.id"] || sample.Used != wantUsed || sample.Reserved != wantReserved[task] || sample.Account != account || sample.Environment != environment || sample.Cluster != "main" || sample.Service != "checkout-api" {
					t.Fatalf("manifest does not match export: %+v", sample)
				}
			}
		}
	}
	if core != 18 || controls != 12 {
		t.Fatalf("got %d core and %d control points", core, controls)
	}
}

func TestPayloadRoundTripAndPrivateMetadata(t *testing.T) {
	f := fixtureForTest(t, ScopedCPU)
	jsonData, err := protojson.Marshal(f.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{f.Manifest.RunID, f.Manifest.ScenarioID, "scenario", "runId", "referenceTime", "expected", "eval", "controls", "samples", "aws", "process.command"} {
		if strings.Contains(string(jsonData), private) {
			t.Fatalf("export leaked private/evaluation metadata: %s", private)
		}
	}
	var roundTrip collectorpb.ExportMetricsServiceRequest
	if err := protojson.Unmarshal(jsonData, &roundTrip); err != nil || !proto.Equal(f.Metrics, &roundTrip) {
		t.Fatalf("JSON round-trip changed payload: %v", err)
	}
	binary, err := proto.Marshal(f.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(binary, &roundTrip); err != nil || !proto.Equal(f.Metrics, &roundTrip) {
		t.Fatalf("protobuf round-trip changed payload: %v", err)
	}
}

func TestTwoFreshPlansNormalizeIdentically(t *testing.T) {
	wantVariableFields := []string{"trace_id", "span_id", "order_id", "transaction_id", "tracking_id", "message_id", "timestamps", "durations"}
	for _, world := range []string{CleanCheckout, ScopedCPU} {
		a := fixtureForTest(t, world)
		if !reflect.DeepEqual(a.Manifest.ExpectedFacts.AllowedVariableFields, wantVariableFields) {
			t.Fatal("normalization must cover email message IDs without masking user IDs or errors")
		}
		reference, _ := time.Parse(time.RFC3339, a.Manifest.ReferenceTime)
		b, err := Build(world, "private-run-702", reference.Add(24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a.Manifest.ExpectedFacts, b.Manifest.ExpectedFacts) || !reflect.DeepEqual(a.Manifest.RequiredServices, b.Manifest.RequiredServices) {
			t.Fatal("fresh run changed normalized plan or declared facts")
		}
		for i, left := range a.Manifest.RequestPlan {
			right := b.Manifest.RequestPlan[i]
			if left.TraceParent == right.TraceParent {
				t.Fatal("fresh runs must have distinct trace identities")
			}
			right.TraceParent = left.TraceParent
			if left != right {
				t.Fatal("fresh run changed normalized request plan")
			}
		}
		for i := range a.Manifest.Samples {
			left, right := a.Manifest.Samples[i], b.Manifest.Samples[i]
			if left.Task == right.Task || left.TimestampUnixNano == right.TimestampUnixNano {
				t.Fatal("fresh runs must have distinct task identities and instants")
			}
			right.Task, right.TimestampUnixNano = left.Task, left.TimestampUnixNano
			if left != right {
				t.Fatal("fresh run changed sample facts")
			}
		}
		again := fixtureForTest(t, world)
		if !reflect.DeepEqual(a.Manifest, again.Manifest) || !proto.Equal(a.Metrics, again.Metrics) {
			t.Fatal("identical arguments did not produce identical output")
		}
	}
}

func TestCleanWorldPlan(t *testing.T) {
	f := fixtureForTest(t, CleanCheckout)
	if len(f.Manifest.Samples) != 0 || len(f.Manifest.Controls) != 0 || len(f.Metrics.ResourceMetrics) != 0 || len(f.Manifest.RequestPlan) != 3 || len(f.Manifest.RequiredServices) != 13 {
		t.Fatalf("unexpected clean plan: %+v", f.Manifest)
	}
	seen := map[string]bool{}
	for _, request := range f.Manifest.RequestPlan {
		if request.Method != "POST" || request.URL != "http://127.0.0.1:8080/api/checkout" || request.Body != "" {
			t.Fatalf("unexpected request: %v", request)
		}
		parts := strings.Split(request.TraceParent, "-")
		if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || parts[3] != "01" || seen[request.TraceParent] {
			t.Fatalf("invalid or duplicate traceparent: %s", request.TraceParent)
		}
		seen[request.TraceParent] = true
	}
	if f.Manifest.TelemetryVerified || len(f.Manifest.ExpectedFacts.RequiredServiceNames) != 13 || f.Manifest.ExpectedFacts.CheckoutCount != 3 || f.Manifest.ExpectedFacts.RequestMethod != "POST" || f.Manifest.ExpectedFacts.RequestRoute != "/api/checkout" || f.Manifest.ExpectedFacts.HTTPStatus != 200 {
		t.Fatal("manifest must declare expected facts without claiming telemetry verification")
	}
	data, _ := json.Marshal(f.Manifest)
	if !strings.Contains(string(data), `"samples":[]`) || !strings.Contains(string(data), `"controls":[]`) {
		t.Fatal("empty arrays must not be null")
	}
}

func TestBuildRejectsInvalidInputs(t *testing.T) {
	reference := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		world, runID string
		reference    time.Time
	}{
		{"unknown", "run", reference}, {ScopedCPU, "", reference}, {ScopedCPU, "bad\nrun", reference},
		{ScopedCPU, strings.Repeat("a", 129), reference}, {ScopedCPU, "run", time.Time{}},
		{ScopedCPU, "run", reference.Add(time.Nanosecond)}, {ScopedCPU, "run", time.Unix(100, 0)},
		{ScopedCPU, "run", time.Date(2400, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if _, err := Build(tc.world, tc.runID, tc.reference); err == nil {
			t.Fatalf("accepted invalid arguments: %+v", tc)
		}
	}
}
