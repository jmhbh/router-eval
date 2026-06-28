package metrics

import (
	"testing"
	"time"

	"router-eval/internal/artifacts"
)

func TestFromRequests(t *testing.T) {
	start := time.Unix(1, 0)
	end := start.Add(2 * time.Second)
	records := []artifacts.RequestRecord{
		{
			Success: true,
			Timing:  artifacts.RequestTiming{TTFTMillis: 100, E2EMillis: 1000, FirstByteUnixNano: start.UnixNano(), LastByteUnixNano: end.UnixNano()},
			Usage:   artifacts.Usage{InputTokens: 10, OutputTokens: 20, CostUSD: 0.01},
			Context: artifacts.ContextMetrics{
				InputTokens:        10,
				OutputTokens:       20,
				ToolCallCount:      2,
				ValidToolCallCount: 1,
			},
		},
		{
			Success:    false,
			ErrorClass: "5xx",
			Timing:     artifacts.RequestTiming{TTFTMillis: 200, E2EMillis: 2000},
			Usage:      artifacts.Usage{CostUSD: 0.02},
		},
	}

	rollup := FromRequests("run-1", records)
	if rollup.Context.RequestCount != 2 {
		t.Fatalf("request count=%d", rollup.Context.RequestCount)
	}
	if rollup.Comparable.TotalCostUSD != 0.03 {
		t.Fatalf("cost=%f", rollup.Comparable.TotalCostUSD)
	}
	if rollup.Comparable.SuccessRate != 0.5 {
		t.Fatalf("success rate=%f", rollup.Comparable.SuccessRate)
	}
	if rollup.Comparable.TotalRequestMillis != 3000 {
		t.Fatalf("total request ms=%f", rollup.Comparable.TotalRequestMillis)
	}
	if rollup.Comparable.ErrorClasses["5xx"] != 1 {
		t.Fatalf("errors=%+v", rollup.Comparable.ErrorClasses)
	}
	if rollup.Context.ToolCallCount != 2 || rollup.Comparable.ToolCallValidityRate != 0.5 {
		t.Fatalf("tool call rollup=%+v comparable=%+v", rollup.Context, rollup.Comparable)
	}
}

func TestFromRequestsOnlyAggregatesStreamingThroughput(t *testing.T) {
	start := time.Unix(1, 0)
	end := start.Add(2 * time.Second)
	records := []artifacts.RequestRecord{
		{
			Success: true,
			Timing:  artifacts.RequestTiming{FirstByteUnixNano: start.UnixNano(), LastByteUnixNano: end.UnixNano()},
			Usage:   artifacts.Usage{OutputTokens: 100},
			Diagnostics: artifacts.Diagnostics{Headers: map[string][]string{
				"Content-Type": []string{"application/json"},
			}},
		},
	}
	rollup := FromRequests("run-1", records)
	if rollup.Comparable.OutputTokensPerSec != 0 {
		t.Fatalf("non-streaming throughput=%f", rollup.Comparable.OutputTokensPerSec)
	}

	records[0].Diagnostics.Headers["Content-Type"] = []string{"text/event-stream"}
	rollup = FromRequests("run-1", records)
	if rollup.Comparable.OutputTokensPerSec != 50 {
		t.Fatalf("streaming throughput=%f", rollup.Comparable.OutputTokensPerSec)
	}
}
