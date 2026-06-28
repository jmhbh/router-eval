package artifacts

import "time"

type CostState string

const (
	CostStateInlineConfirmed   CostState = "inline_confirmed"
	CostStateUsageAPIConfirmed CostState = "usage_api_confirmed"
	CostStatePending           CostState = "pending"
	CostStateEstimated         CostState = "estimated"
	CostStateUnavailable       CostState = "unavailable"
)

type RunStatus string

const (
	RunStatusRunning RunStatus = "running"
	RunStatusDone    RunStatus = "done"
	RunStatusFailed  RunStatus = "failed"
)

type WorkloadRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type Manifest struct {
	RunID     string            `json:"run_id"`
	Router    string            `json:"router"`
	Model     string            `json:"model,omitempty"`
	Workload  WorkloadRef       `json:"workload"`
	Status    RunStatus         `json:"status"`
	StartedAt time.Time         `json:"started_at"`
	EndedAt   *time.Time        `json:"ended_at,omitempty"`
	Config    map[string]string `json:"config,omitempty"`
}

type RequestRecord struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	RequestID     string    `json:"request_id,omitempty"`
	GenerationID  string    `json:"generation_id,omitempty"`
	Router        string    `json:"router"`
	Method        string    `json:"method"`
	Endpoint      string    `json:"endpoint"`
	StatusCode    int       `json:"status_code"`
	Success       bool      `json:"success"`
	ErrorClass    string    `json:"error_class,omitempty"`
	Error         string    `json:"error,omitempty"`
	StartedAt     time.Time `json:"started_at"`

	Timing RequestTiming `json:"timing"`
	Usage  Usage         `json:"usage,omitempty"`

	Comparable  ComparableMetrics `json:"comparable"`
	Context     ContextMetrics    `json:"context,omitempty"`
	Diagnostics Diagnostics       `json:"diagnostics,omitempty"`
}

type RequestTiming struct {
	RequestSentUnixNano int64   `json:"request_sent_unix_nano"`
	FirstByteUnixNano   int64   `json:"first_byte_unix_nano,omitempty"`
	LastByteUnixNano    int64   `json:"last_byte_unix_nano,omitempty"`
	TTFTMillis          float64 `json:"ttft_ms,omitempty"`
	E2EMillis           float64 `json:"e2e_ms,omitempty"`
}

type Usage struct {
	InputTokens         int       `json:"input_tokens,omitempty"`
	OutputTokens        int       `json:"output_tokens,omitempty"`
	CacheCreationTokens int       `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int       `json:"cache_read_tokens,omitempty"`
	TotalTokens         int       `json:"total_tokens,omitempty"`
	CostUSD             float64   `json:"cost_usd,omitempty"`
	CostKnown           bool      `json:"cost_known,omitempty"`
	CostState           CostState `json:"cost_state"`
	Raw                 any       `json:"raw,omitempty"`
}

type ComparableMetrics struct {
	CostUSD              float64 `json:"cost_usd,omitempty"`
	TTFTMillis           float64 `json:"ttft_ms,omitempty"`
	E2EMillis            float64 `json:"e2e_ms,omitempty"`
	OutputTokensPerSec   float64 `json:"output_tokens_per_sec,omitempty"`
	ToolCallValidityRate float64 `json:"tool_call_validity_rate,omitempty"`
}

type ToolCallMetrics struct {
	Count      int `json:"count,omitempty"`
	ValidCount int `json:"valid_count,omitempty"`
}

type ToolCallDetail struct {
	ID             string `json:"id,omitempty"`
	Type           string `json:"type,omitempty"`
	Name           string `json:"name,omitempty"`
	Arguments      string `json:"arguments,omitempty"`
	ArgumentsValid bool   `json:"arguments_valid"`
}

type ContextMetrics struct {
	InputTokens        int `json:"input_tokens,omitempty"`
	OutputTokens       int `json:"output_tokens,omitempty"`
	RequestBytes       int `json:"request_bytes,omitempty"`
	ResponseBytes      int `json:"response_bytes,omitempty"`
	ToolCallCount      int `json:"tool_call_count,omitempty"`
	ValidToolCallCount int `json:"valid_tool_call_count,omitempty"`
}

type Diagnostics struct {
	Headers        map[string][]string `json:"headers,omitempty"`
	ParserWarnings []string            `json:"parser_warnings,omitempty"`
	RawCapturePath string              `json:"raw_capture_path,omitempty"`
	RouterSpecific map[string]any      `json:"router_specific,omitempty"`
	ToolCalls      []ToolCallDetail    `json:"tool_calls,omitempty"`
}

type Metrics struct {
	RunID       string              `json:"run_id"`
	UpdatedAt   time.Time           `json:"updated_at"`
	Comparable  RunComparableRollup `json:"comparable"`
	Context     RunContextRollup    `json:"context,omitempty"`
	Diagnostics map[string]any      `json:"diagnostics,omitempty"`
}

type RunComparableRollup struct {
	TotalCostUSD             float64        `json:"total_cost_usd,omitempty"`
	TotalCostKnown           bool           `json:"total_cost_known,omitempty"`
	CostSampleCount          int            `json:"cost_sample_count,omitempty"`
	CostPerSuccessfulRequest float64        `json:"cost_per_successful_request,omitempty"`
	TTFTP50Millis            float64        `json:"ttft_p50_ms,omitempty"`
	E2EP50Millis             float64        `json:"e2e_p50_ms,omitempty"`
	E2EP95Millis             float64        `json:"e2e_p95_ms,omitempty"`
	TotalRequestMillis       float64        `json:"total_request_ms,omitempty"`
	OutputTokensPerSec       float64        `json:"output_tokens_per_sec,omitempty"`
	SuccessRate              float64        `json:"success_rate,omitempty"`
	ToolCallValidityRate     float64        `json:"tool_call_validity_rate,omitempty"`
	ErrorClasses             map[string]int `json:"error_classes,omitempty"`
}

type RunContextRollup struct {
	RequestCount          int     `json:"request_count"`
	InputTokens           int     `json:"input_tokens,omitempty"`
	OutputTokens          int     `json:"output_tokens,omitempty"`
	ToolCallCount         int     `json:"tool_call_count,omitempty"`
	ValidToolCallCount    int     `json:"valid_tool_call_count,omitempty"`
	CodexWallMillis       float64 `json:"codex_wall_ms,omitempty"`
	CodexRouterBusyMillis float64 `json:"codex_router_busy_ms,omitempty"`
}

type Index struct {
	UpdatedAt time.Time    `json:"updated_at"`
	Runs      []RunCatalog `json:"runs"`
}

type RunCatalog struct {
	RunID     string            `json:"run_id"`
	Router    string            `json:"router"`
	Model     string            `json:"model,omitempty"`
	Workload  WorkloadRef       `json:"workload"`
	Status    RunStatus         `json:"status"`
	StartedAt time.Time         `json:"started_at"`
	EndedAt   *time.Time        `json:"ended_at,omitempty"`
	Summary   RunCatalogSummary `json:"summary,omitempty"`
}

type RunCatalogSummary struct {
	TotalCostUSD       float64 `json:"total_cost_usd,omitempty"`
	TotalCostKnown     bool    `json:"total_cost_known,omitempty"`
	TTFTP50Millis      float64 `json:"ttft_p50_ms,omitempty"`
	E2EP50Millis       float64 `json:"e2e_p50_ms,omitempty"`
	E2EP95Millis       float64 `json:"e2e_p95_ms,omitempty"`
	TotalRequestMillis float64 `json:"total_request_ms,omitempty"`
	SuccessRate        float64 `json:"success_rate,omitempty"`
	RequestCount       int     `json:"request_count,omitempty"`
	InputTokens        int     `json:"input_tokens,omitempty"`
	OutputTokens       int     `json:"output_tokens,omitempty"`
	ToolCallCount      int     `json:"tool_call_count,omitempty"`
}

type UIAggregate struct {
	UpdatedAt time.Time          `json:"updated_at"`
	Probes    []ProbeRouterGroup `json:"probes"`
	Harnesses []HarnessGroup     `json:"harnesses"`
}

type ProbeRouterGroup struct {
	Router    string               `json:"router"`
	Summary   AggregateSummary     `json:"summary"`
	Workloads []ProbeWorkloadGroup `json:"workloads"`
}

type ProbeWorkloadGroup struct {
	Type    string           `json:"type"`
	Summary AggregateSummary `json:"summary"`
	Probes  []ProbeRunGroup  `json:"probes"`
}

type ProbeRunGroup struct {
	Name    string           `json:"name"`
	Runs    []RunCatalog     `json:"runs"`
	Summary AggregateSummary `json:"summary"`
}

type HarnessGroup struct {
	Harness string               `json:"harness"`
	Summary AggregateSummary     `json:"summary"`
	Runs    []RunCatalog         `json:"runs,omitempty"`
	Routers []HarnessRouterGroup `json:"routers"`
}

type HarnessRouterGroup struct {
	Router  string             `json:"router"`
	Summary AggregateSummary   `json:"summary"`
	Tasks   []HarnessTaskGroup `json:"tasks"`
}

type HarnessTaskGroup struct {
	Task    string           `json:"task"`
	Runs    []RunCatalog     `json:"runs"`
	Summary AggregateSummary `json:"summary"`
}

type AggregateSummary struct {
	RunCount           int     `json:"run_count"`
	RequestCount       int     `json:"request_count,omitempty"`
	TotalCostUSD       float64 `json:"total_cost_usd,omitempty"`
	TotalCostKnown     bool    `json:"total_cost_known,omitempty"`
	TTFTP50Millis      float64 `json:"ttft_p50_ms,omitempty"`
	E2EP50Millis       float64 `json:"e2e_p50_ms,omitempty"`
	E2EP95Millis       float64 `json:"e2e_p95_ms,omitempty"`
	TotalRequestMillis float64 `json:"total_request_ms,omitempty"`
	SuccessRate        float64 `json:"success_rate,omitempty"`
	InputTokens        int     `json:"input_tokens,omitempty"`
	OutputTokens       int     `json:"output_tokens,omitempty"`
	ToolCallCount      int     `json:"tool_call_count,omitempty"`
}
