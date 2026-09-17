package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"
)

const maxResponseBytes = 1 << 20

// ValidateEndpoint accepts only an explicit local OTLP/HTTP metrics endpoint.
func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Port() == "" || u.Path != "/v1/metrics" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("endpoint must be an explicit http://local-host:port/v1/metrics URL without credentials or query parameters")
	}
	if !localHost(u.Hostname(), true) {
		return fmt.Errorf("endpoint host must be a loopback address, localhost or otel-collector")
	}
	return nil
}

func localHost(host string, collector bool) bool {
	if host == "localhost" || (collector && host == "otel-collector") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func client() *http.Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			// An inherited HTTP proxy must never forward these local payloads elsewhere.
			Proxy: nil,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(address)
				if err != nil || !localHost(host, true) {
					return nil, fmt.Errorf("only local destinations are allowed")
				}
				if host == "localhost" {
					address = net.JoinHostPort("127.0.0.1", port)
				}
				return dialer.DialContext(ctx, network, address)
			},
		},
	}
}

// Execute is intentionally separate from Build. The command additionally requires --execute.
// It performs one readiness pass, three ordered requests and at most one metrics export.
func Execute(ctx context.Context, fixture *Fixture, endpoint string) error {
	if os.Getenv("EVAL_MODE") != "1" {
		return fmt.Errorf("execution requires EVAL_MODE=1")
	}
	if fixture == nil {
		return fmt.Errorf("fixture is required")
	}
	if fixture.Manifest.ScenarioID == ScopedCPU {
		if err := ValidateEndpoint(endpoint); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	c := client()
	defer c.CloseIdleConnections()
	for _, service := range fixture.Manifest.RequiredServices {
		if err := checkJSONStatus(ctx, c, Request{Method: "GET", URL: service.HealthURL}, "ok"); err != nil {
			return fmt.Errorf("required service %s: %w", service.Name, err)
		}
	}
	for i, request := range fixture.Manifest.RequestPlan {
		if err := checkJSONStatus(ctx, c, request, "order_placed"); err != nil {
			return fmt.Errorf("checkout request %d: %w", i+1, err)
		}
	}
	if fixture.Manifest.ScenarioID == ScopedCPU {
		return export(ctx, c, endpoint, fixture.Metrics)
	}
	return nil
}

func readResponse(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	return body, nil
}

func checkJSONStatus(ctx context.Context, c *http.Client, plan Request, want string) error {
	u, err := url.Parse(plan.URL)
	if err != nil || u.Scheme != "http" || u.User != nil || !localHost(u.Hostname(), false) || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("service requests require a loopback HTTP URL without credentials or query parameters")
	}
	request, err := http.NewRequestWithContext(ctx, plan.Method, plan.URL, strings.NewReader(plan.Body))
	if err != nil {
		return err
	}
	if plan.TraceParent != "" {
		request.Header.Set("traceparent", plan.TraceParent)
	}
	response, err := c.Do(request)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	data, err := readResponse(response)
	if err != nil {
		return err
	}
	var result struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("invalid JSON status response: %w", err)
	}
	if result.Status != want || result.Error != "" {
		return fmt.Errorf("response did not confirm %s", want)
	}
	return nil
}

func export(ctx context.Context, c *http.Client, endpoint string, metrics *collectorpb.ExportMetricsServiceRequest) error {
	if err := ValidateEndpoint(endpoint); err != nil {
		return err
	}
	body, err := proto.Marshal(metrics)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-protobuf")
	response, err := c.Do(request)
	if err != nil {
		return fmt.Errorf("metrics export failed: %w", err)
	}
	data, err := readResponse(response)
	if err != nil {
		return err
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/x-protobuf" {
		return fmt.Errorf("metrics response must have application/x-protobuf content type")
	}
	var result collectorpb.ExportMetricsServiceResponse
	if err := proto.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("invalid metrics response: %w", err)
	}
	if partial := result.GetPartialSuccess(); partial != nil && (partial.RejectedDataPoints != 0 || partial.ErrorMessage != "") {
		return fmt.Errorf("metrics export did not confirm full acceptance (rejected points: %d)", partial.RejectedDataPoints)
	}
	return nil
}
