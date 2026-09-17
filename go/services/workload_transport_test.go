package services

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

type responseTransport struct {
	status int
	body   string
	calls  int
}

func (t *responseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{StatusCode: t.status, Body: io.NopCloser(strings.NewReader(t.body)), Header: make(http.Header)}, nil
}

func TestWorkloadTransportChecksFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		pass   bool
	}{
		{"valid", 200, `{"status":"ok"}`, true},
		{"failure", 500, `{"error":"unavailable"}`, false},
		{"hidden_failure", 200, `{"error":"unavailable"}`, false},
		{"invalid", 200, `not JSON`, false},
		{"oversized", 200, strings.Repeat(" ", 1<<20) + `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := &responseTransport{status: tc.status, body: tc.body}
			checked := &workloadTransport{base: base}
			req, _ := http.NewRequest("GET", "http://127.0.0.1:8081/charge", nil)
			resp, err := checked.RoundTrip(req)
			if (err == nil) != tc.pass {
				t.Fatalf("err=%v", err)
			}
			if tc.pass {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if !bytes.Equal(b, []byte(tc.body)) {
					t.Fatal("response changed")
				}
			} else if checked.failure == nil {
				t.Fatal("failure not retained")
			}
		})
	}
}

func TestWorkloadTransportRejectsRemoteWithoutRequest(t *testing.T) {
	base := &responseTransport{status: 200, body: `{}`}
	for _, target := range []string{"https://localhost:443/", "http://example.com/", "http://user:password@localhost/"} {
		checked := &workloadTransport{base: base}
		req, _ := http.NewRequest("GET", target, nil)
		if _, err := checked.RoundTrip(req); err == nil {
			t.Fatal("accepted remote target")
		}
	}
	if base.calls != 0 {
		t.Fatal("made a remote request")
	}
}
