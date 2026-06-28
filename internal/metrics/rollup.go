package metrics

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"router-eval/internal/artifacts"
)

func FromRequests(runID string, records []artifacts.RequestRecord) artifacts.Metrics {
	var ttfts []float64
	var e2es []float64
	var totalRequestMillis float64
	var totalCost float64
	var costSamples int
	var successful int
	var inputTokens int
	var outputTokens int
	var toolCalls int
	var validToolCalls int
	errorClasses := map[string]int{}

	for _, record := range records {
		if record.Timing.TTFTMillis > 0 {
			ttfts = append(ttfts, record.Timing.TTFTMillis)
		}
		if record.Timing.E2EMillis > 0 {
			e2es = append(e2es, record.Timing.E2EMillis)
			totalRequestMillis += record.Timing.E2EMillis
		}
		if requestSucceeded(record) {
			successful++
		}
		if record.ErrorClass != "" {
			errorClasses[record.ErrorClass]++
		}
		if usageCostKnown(record.Usage) {
			totalCost += record.Usage.CostUSD
			costSamples++
		}
		inputTokens += record.Context.InputTokens
		outputTokens += record.Context.OutputTokens
		toolCalls += record.Context.ToolCallCount
		validToolCalls += record.Context.ValidToolCallCount
	}

	var costPerSuccess float64
	if successful > 0 {
		costPerSuccess = totalCost / float64(successful)
	}

	var successRate float64
	if len(records) > 0 {
		successRate = float64(successful) / float64(len(records))
	}
	var toolValidity float64
	if toolCalls > 0 {
		toolValidity = float64(validToolCalls) / float64(toolCalls)
	}

	return artifacts.Metrics{
		RunID:     runID,
		UpdatedAt: time.Now().UTC(),
		Comparable: artifacts.RunComparableRollup{
			TotalCostUSD:             totalCost,
			TotalCostKnown:           costSamples > 0,
			CostSampleCount:          costSamples,
			CostPerSuccessfulRequest: costPerSuccess,
			TTFTP50Millis:            percentile(ttfts, 0.50),
			E2EP50Millis:             percentile(e2es, 0.50),
			E2EP95Millis:             percentile(e2es, 0.95),
			TotalRequestMillis:       totalRequestMillis,
			OutputTokensPerSec:       aggregateThroughput(records),
			SuccessRate:              successRate,
			ToolCallValidityRate:     toolValidity,
			ErrorClasses:             errorClasses,
		},
		Context: artifacts.RunContextRollup{
			RequestCount:       len(records),
			InputTokens:        inputTokens,
			OutputTokens:       outputTokens,
			ToolCallCount:      toolCalls,
			ValidToolCallCount: validToolCalls,
		},
	}
}

func requestSucceeded(record artifacts.RequestRecord) bool {
	if record.Success {
		return true
	}
	return (record.ErrorClass == "stream" || record.ErrorClass == "client_cancel") &&
		record.Error == "context canceled" &&
		record.Timing.FirstByteUnixNano > 0 &&
		record.Context.ResponseBytes > 0
}

func usageCostKnown(usage artifacts.Usage) bool {
	if usage.CostKnown || usage.CostUSD != 0 {
		return true
	}
	if raw, ok := usage.Raw.(map[string]any); ok {
		_, ok = raw["cost"]
		if ok {
			return true
		}
		_, ok = raw["cost_usd"]
		if ok {
			return true
		}
		_, ok = raw["total_cost"]
		return ok
	}
	return false
}

func ReadRequestJSONL(path string) ([]artifacts.RequestRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []artifacts.RequestRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var record artifacts.RequestRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p * float64(len(sorted)-1)
	lower := int(pos)
	upper := lower + 1
	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	weight := pos - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func aggregateThroughput(records []artifacts.RequestRecord) float64 {
	var tokens int
	var seconds float64
	for _, record := range records {
		if !isStreaming(record) || record.Usage.OutputTokens <= 0 || record.Timing.FirstByteUnixNano == 0 || record.Timing.LastByteUnixNano == 0 {
			continue
		}
		d := time.Unix(0, record.Timing.LastByteUnixNano).Sub(time.Unix(0, record.Timing.FirstByteUnixNano))
		if d <= 0 {
			continue
		}
		tokens += record.Usage.OutputTokens
		seconds += d.Seconds()
	}
	if tokens == 0 || seconds <= 0 {
		return 0
	}
	return float64(tokens) / seconds
}

func isStreaming(record artifacts.RequestRecord) bool {
	for key, values := range record.Diagnostics.Headers {
		if strings.EqualFold(key, "Content-Type") {
			for _, value := range values {
				if strings.Contains(strings.ToLower(value), "text/event-stream") {
					return true
				}
			}
		}
	}
	return false
}
