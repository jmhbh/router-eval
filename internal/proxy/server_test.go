package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type testRouter struct{}

func (testRouter) Name() string { return "test-router" }
func (testRouter) InjectAuth(req *http.Request) error {
	req.Header.Set("Authorization", "Bearer upstream-secret")
	return nil
}
func (testRouter) CaptureIDs(headers http.Header) (string, string, map[string]any) {
	return headers.Get("X-Test-Request-Id"), "", map[string]any{"x_test_request_id": headers.Get("X-Test-Request-Id")}
}

func TestProxyForwardsAndRecordsRequest(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Fatalf("authorization was not injected: %q", got)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"X-Test-Request-Id": []string{"req-1"},
				"Content-Type":      []string{"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl-1","usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5,"cost":0.01}}`)),
		}, nil
	})}

	storeRoot := t.TempDir()
	server, err := NewServer(Config{
		Addr:        "127.0.0.1:0",
		Upstream:    "https://upstream.example",
		OutDir:      storeRoot,
		RunID:       "run-1",
		Router:      testRouter{},
		HTTPClient:  client,
		ProxyAPIKey: "local-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), `"chatcmpl-1"`) {
		t.Fatalf("unexpected body: %s", body)
	}

	records, err := os.ReadFile(storeRoot + "/runs/run-1/requests.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(records), `"request_id":"chatcmpl-1"`) {
		t.Fatalf("record did not include parsed id: %s", records)
	}
	if !strings.Contains(string(records), `"cost_state":"inline_confirmed"`) {
		t.Fatalf("record did not include inline cost state: %s", records)
	}
	if strings.Contains(string(records), `"output_tokens_per_sec"`) {
		t.Fatalf("non-streaming record should not include throughput: %s", records)
	}
}

func TestProxyParsesGzipCopiedBodyOutOfBand(t *testing.T) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	_, _ = gzipWriter.Write([]byte(`{"id":"resp-1","usage":{"input_tokens":7,"output_tokens":11,"total_tokens":18,"cost":0.02}}`))
	_ = gzipWriter.Close()

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":     []string{"application/json"},
				"Content-Encoding": []string{"gzip"},
			},
			Body: io.NopCloser(bytes.NewReader(compressed.Bytes())),
		}, nil
	})}

	storeRoot := t.TempDir()
	server, err := NewServer(Config{
		Addr:        "127.0.0.1:0",
		Upstream:    "https://upstream.example",
		OutDir:      storeRoot,
		RunID:       "run-gzip",
		Router:      testRouter{},
		HTTPClient:  client,
		ProxyAPIKey: "local-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	records, err := os.ReadFile(storeRoot + "/runs/run-gzip/requests.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(records), `"request_id":"resp-1"`) {
		t.Fatalf("record did not include parsed gzip id: %s", records)
	}
	if !strings.Contains(string(records), `"input_tokens":7`) || !strings.Contains(string(records), `"cost_state":"inline_confirmed"`) {
		t.Fatalf("record did not include parsed gzip usage: %s", records)
	}
}

func TestProxyRejectsMissingDownstreamAuth(t *testing.T) {
	server, err := NewServer(Config{
		Upstream: "https://upstream.example",
		OutDir:   t.TempDir(),
		RunID:    "run-auth",
		Router:   testRouter{},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Fatal("upstream should not be called")
			return nil, nil
		})},
		ProxyAPIKey: "local-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestProxyStreamCancelPreservesUpstreamStatus(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: &cancelAfterChunkReadCloser{chunk: []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-cancel\"}}\n\n")},
		}, nil
	})}

	storeRoot := t.TempDir()
	server, err := NewServer(Config{
		Addr:        "127.0.0.1:0",
		Upstream:    "https://upstream.example",
		OutDir:      storeRoot,
		RunID:       "run-cancel",
		Router:      testRouter{},
		HTTPClient:  client,
		ProxyAPIKey: "local-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	records, err := os.ReadFile(storeRoot + "/runs/run-cancel/requests.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	text := string(records)
	if !strings.Contains(text, `"status_code":200`) {
		t.Fatalf("stream cancel did not preserve status: %s", text)
	}
	if !strings.Contains(text, `"success":true`) {
		t.Fatalf("stream cancel did not preserve success: %s", text)
	}
	if !strings.Contains(text, `"error_class":"client_cancel"`) {
		t.Fatalf("stream cancel was not classified as client_cancel: %s", text)
	}
	if !strings.Contains(text, `"e2e_ms":`) {
		t.Fatalf("stream cancel did not include e2e timing: %s", text)
	}
	if !strings.Contains(text, `"request_id":"resp-cancel"`) {
		t.Fatalf("stream cancel did not parse response id: %s", text)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type cancelAfterChunkReadCloser struct {
	chunk []byte
	read  bool
}

func (r *cancelAfterChunkReadCloser) Read(p []byte) (int, error) {
	if r.read {
		return 0, context.Canceled
	}
	r.read = true
	return copy(p, r.chunk), context.Canceled
}

func (r *cancelAfterChunkReadCloser) Close() error {
	return nil
}
