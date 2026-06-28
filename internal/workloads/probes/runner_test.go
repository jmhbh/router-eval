package probes

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRunnerPostsToProxy(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "http://proxy.local/v1/responses" {
			t.Fatalf("url=%s", req.URL.String())
		}
		if req.Header.Get("Authorization") != "Bearer local-key" {
			t.Fatalf("missing local proxy auth")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})}

	result, err := Runner{
		ProxyBaseURL: "http://proxy.local",
		HTTPClient:   client,
		APIKey:       "local-key",
	}.Run(context.Background(), Request{
		Name:     "simple_request",
		Endpoint: "/v1/responses",
		Payload:  map[string]any{"model": "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK || result.BodyBytes == 0 {
		t.Fatalf("result=%+v", result)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
