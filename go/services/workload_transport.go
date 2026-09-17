package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

// Eval checkout must not turn ignored downstream failures into a healthy fixture.
type workloadTransport struct {
	base    http.RoundTripper
	failure error
}

func (t *workloadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	fail := func(err error) (*http.Response, error) {
		if t.failure == nil {
			t.failure = err
		}
		return nil, err
	}
	host := req.URL.Hostname()
	if req.URL.Scheme != "http" || req.URL.User != nil || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
		return fail(fmt.Errorf("workload dependency must be loopback HTTP"))
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return fail(err)
	}
	if resp.StatusCode != http.StatusOK || len(body) > 1<<20 || !json.Valid(body) {
		return fail(fmt.Errorf("invalid workload dependency response (HTTP %d)", resp.StatusCode))
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) == nil {
		if value, ok := object["error"]; ok && string(value) != "null" && string(value) != `""` {
			return fail(fmt.Errorf("workload dependency returned an error payload"))
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}
