package tokenrouter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUsageClientLookupUsesCSVExport(t *testing.T) {
	client := &UsageClient{
		BaseURL: "https://tokenrouter.example",
		APIKey:  "management-key",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/api/management/export/hour/usages" {
				t.Fatalf("path=%s", req.URL.Path)
			}
			if got := req.URL.Query().Get("start_timestamp"); got != "" {
				t.Fatalf("unexpected start_timestamp=%s", got)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer management-key" {
				t.Fatalf("auth=%q", got)
			}
			body := "request_id,model_name,prompt_tokens,completion_tokens,cache_creation_tokens,cache_tokens,cost,created_at\n" +
				"req-1,m,10,5,2,3,0.012,2026-06-27T23:00:00Z\n"
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
	}
	result, err := client.Lookup(context.Background(), "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.Usage.CostUSD != 0.012 || result.Usage.TotalTokens != 20 || !result.Usage.CostKnown {
		t.Fatalf("result=%+v", result)
	}
}

func TestParseUsageCSVAcceptsAlternateHeaders(t *testing.T) {
	rows, headers, err := ParseUsageCSV(strings.NewReader("id,model,input_tokens,output_tokens,total_cost\nreq-1,m,11,7,0.02\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%+v", rows)
	}
	if rows[0].RequestID != "req-1" || rows[0].PromptTokens != 11 || rows[0].CompletionTokens != 7 || rows[0].Cost != 0.02 {
		t.Fatalf("row=%+v", rows[0])
	}
	if strings.Join(headers, ",") != "id,model,input_tokens,output_tokens,total_cost" {
		t.Fatalf("headers=%+v", headers)
	}
}

func TestUsageClientExportUsesTimestampWindow(t *testing.T) {
	client := &UsageClient{
		BaseURL: "https://tokenrouter.example",
		APIKey:  "management-key",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.URL.Query().Get("start_timestamp"); got != "1716200000" {
				t.Fatalf("start_timestamp=%s", got)
			}
			if got := req.URL.Query().Get("end_timestamp"); got != "1716300000" {
				t.Fatalf("end_timestamp=%s", got)
			}
			body := "request_id,cost\nreq-1,0.012\n"
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
	}
	export, err := client.Export(context.Background(), TimeWindow{StartUnix: 1716200000, EndUnix: 1716300000})
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Rows) != 1 || !strings.Contains(string(export.RawCSV), "req-1") {
		t.Fatalf("export=%+v raw=%s", export, export.RawCSV)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
