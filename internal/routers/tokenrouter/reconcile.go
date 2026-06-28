package tokenrouter

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"router-eval/internal/artifacts"
	"router-eval/internal/usage"
)

type UsageClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func NewUsageClient(baseURL string) *UsageClient {
	return &UsageClient{
		BaseURL: baseURL,
		APIKey:  managementKeyFromEnv(),
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func managementKeyFromEnv() string {
	return os.Getenv("MGMT_KEY")
}

func (c *UsageClient) Lookup(ctx context.Context, requestID string) (usage.LookupResult, error) {
	if requestID == "" {
		return usage.LookupResult{}, fmt.Errorf("request id is required")
	}
	export, err := c.Export(ctx, TimeWindow{})
	if err != nil {
		return usage.LookupResult{}, err
	}
	for _, row := range export.Rows {
		if row.RequestID == requestID {
			return row.LookupResult(), nil
		}
	}
	return usage.LookupResult{RequestID: requestID, Found: false}, nil
}

func (c *UsageClient) Export(ctx context.Context, window TimeWindow) (UsageExport, error) {
	if c.APIKey == "" {
		return UsageExport{}, fmt.Errorf("MGMT_KEY is not set")
	}
	endpoint, err := url.Parse(c.BaseURL)
	if err != nil {
		return UsageExport{}, err
	}
	endpoint.Path = "/api/management/export/hour/usages"
	q := endpoint.Query()
	if window.StartUnix > 0 {
		q.Set("start_timestamp", strconv.FormatInt(window.StartUnix, 10))
	}
	if window.EndUnix > 0 {
		q.Set("end_timestamp", strconv.FormatInt(window.EndUnix, 10))
	}
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return UsageExport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return UsageExport{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return UsageExport{}, fmt.Errorf("tokenRouter usage export returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return UsageExport{}, err
	}
	rows, headers, err := ParseUsageCSV(bytes.NewReader(raw))
	if err != nil {
		return UsageExport{}, err
	}
	return UsageExport{
		Rows:    rows,
		Headers: headers,
		RawCSV:  raw,
		URL:     endpoint.String(),
		Window:  window,
	}, nil
}

func ParseUsageCSV(r io.Reader) ([]UsageRow, []string, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(records) == 0 {
		return nil, nil, nil
	}
	header := map[string]int{}
	headers := make([]string, 0, len(records[0]))
	for i, name := range records[0] {
		normalized := normalizeCSVHeader(name)
		header[normalized] = i
		headers = append(headers, normalized)
	}
	rows := make([]UsageRow, 0, len(records)-1)
	for _, record := range records[1:] {
		row := UsageRow{
			RequestID:           csvString(record, header, "request_id", "id"),
			ModelName:           csvString(record, header, "model_name", "model"),
			PromptTokens:        csvInt(record, header, "prompt_tokens", "input_tokens"),
			CompletionTokens:    csvInt(record, header, "completion_tokens", "output_tokens"),
			CacheCreationTokens: csvInt(record, header, "cache_creation_tokens", "cache_write_tokens", "input_cache_write_tokens"),
			CacheTokens:         csvInt(record, header, "cache_tokens", "cache_read_tokens", "input_cache_read_tokens"),
			Cost:                csvFloat(record, header, "cost", "cost_usd", "total_cost"),
			CreatedAt:           csvString(record, header, "created_at", "timestamp", "time"),
			Raw:                 map[string]string{},
		}
		for name, idx := range header {
			if idx >= 0 && idx < len(record) {
				row.Raw[name] = record[idx]
			}
		}
		if row.RequestID != "" {
			rows = append(rows, row)
		}
	}
	return rows, headers, nil
}

type TimeWindow struct {
	StartUnix int64 `json:"start_unix,omitempty"`
	EndUnix   int64 `json:"end_unix,omitempty"`
}

type UsageExport struct {
	Rows    []UsageRow `json:"-"`
	Headers []string   `json:"headers,omitempty"`
	RawCSV  []byte     `json:"-"`
	URL     string     `json:"url,omitempty"`
	Window  TimeWindow `json:"window,omitempty"`
}

type UsageRow struct {
	RequestID           string            `json:"request_id"`
	ModelName           string            `json:"model_name,omitempty"`
	PromptTokens        int               `json:"prompt_tokens,omitempty"`
	CompletionTokens    int               `json:"completion_tokens,omitempty"`
	CacheCreationTokens int               `json:"cache_creation_tokens,omitempty"`
	CacheTokens         int               `json:"cache_tokens,omitempty"`
	Cost                float64           `json:"cost,omitempty"`
	CreatedAt           string            `json:"created_at,omitempty"`
	Raw                 map[string]string `json:"raw,omitempty"`
}

func (r UsageRow) LookupResult() usage.LookupResult {
	return usage.LookupResult{
		RequestID: r.RequestID,
		Found:     true,
		Raw:       r,
		Usage: artifacts.Usage{
			InputTokens:         r.PromptTokens,
			OutputTokens:        r.CompletionTokens,
			CacheCreationTokens: r.CacheCreationTokens,
			CacheReadTokens:     r.CacheTokens,
			TotalTokens:         r.PromptTokens + r.CompletionTokens + r.CacheCreationTokens + r.CacheTokens,
			CostUSD:             r.Cost,
			CostKnown:           true,
			CostState:           artifacts.CostStateUsageAPIConfirmed,
			Raw:                 r,
		},
	}
}

func normalizeCSVHeader(value string) string {
	value = strings.TrimPrefix(value, "\ufeff")
	value = strings.TrimSpace(strings.ToLower(value))
	replacer := strings.NewReplacer(" ", "_", "-", "_", ".", "_")
	return replacer.Replace(value)
}

func csvString(record []string, header map[string]int, names ...string) string {
	for _, name := range names {
		idx, ok := header[name]
		if ok && idx >= 0 && idx < len(record) {
			return strings.TrimSpace(record[idx])
		}
	}
	return ""
}

func csvInt(record []string, header map[string]int, names ...string) int {
	value := strings.ReplaceAll(csvString(record, header, names...), ",", "")
	n, _ := strconv.Atoi(value)
	return n
}

func csvFloat(record []string, header map[string]int, names ...string) float64 {
	value := strings.ReplaceAll(csvString(record, header, names...), ",", "")
	n, _ := strconv.ParseFloat(value, 64)
	return n
}
