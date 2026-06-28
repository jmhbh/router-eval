package parsers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"router-eval/internal/artifacts"
)

type Result struct {
	ID           string
	GenerationID string
	Usage        artifacts.Usage
	ToolCalls    artifacts.ToolCallMetrics
	ToolDetails  []artifacts.ToolCallDetail
	Warnings     []string
}

func Parse(endpoint, contentType string, body []byte) Result {
	result := Result{
		Usage: artifacts.Usage{CostState: artifacts.CostStatePending},
	}
	if len(bytes.TrimSpace(body)) == 0 {
		result.Usage.CostState = artifacts.CostStateUnavailable
		return result
	}
	if isSSE(contentType, body) {
		return parseSSE(body)
	}
	return parseJSONBody(body)
}

func parseJSONBody(body []byte) Result {
	result := Result{Usage: artifacts.Usage{CostState: artifacts.CostStatePending}}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		result.Warnings = append(result.Warnings, "json parse failed: "+err.Error())
		return result
	}
	if id, ok := stringField(value, "id"); ok {
		result.ID = id
	}
	if usage, ok := findUsage(value); ok {
		result.Usage = normalizeUsage(usage)
	}
	result.ToolDetails = collectToolCalls(value, map[string]bool{})
	result.ToolCalls = summarizeToolCalls(result.ToolDetails)
	return result
}

func parseSSE(body []byte) Result {
	result := Result{Usage: artifacts.Usage{CostState: artifacts.CostStatePending}}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var dataLines []string
	seenToolCalls := map[string]bool{}
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if data == "" || data == "[DONE]" {
			return
		}
		var value any
		if err := json.Unmarshal([]byte(data), &value); err != nil {
			result.Warnings = append(result.Warnings, "sse data parse failed: "+err.Error())
			return
		}
		if id, ok := stringField(value, "id"); ok && result.ID == "" {
			result.ID = id
		}
		if id, ok := nestedStringField(value, "response", "id"); ok {
			result.ID = id
		}
		if usage, ok := findUsage(value); ok {
			result.Usage = normalizeUsage(usage)
		}
		details := collectSSEToolCalls(value, seenToolCalls)
		result.ToolDetails = append(result.ToolDetails, details...)
		result.ToolCalls = summarizeToolCalls(result.ToolDetails)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		result.Warnings = append(result.Warnings, "sse scan failed: "+err.Error())
	}
	return result
}

func collectSSEToolCalls(value any, seen map[string]bool) []artifacts.ToolCallDetail {
	raw, ok := value.(map[string]any)
	if !ok {
		return collectToolCalls(value, seen)
	}
	eventType, _ := raw["type"].(string)
	switch eventType {
	case "response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done":
		return nil
	case "response.output_item.done":
		if item, ok := raw["item"]; ok {
			return collectToolCalls(item, seen)
		}
		return nil
	case "response.completed":
		if response, ok := raw["response"]; ok {
			return collectToolCalls(response, seen)
		}
		return nil
	default:
		return collectToolCalls(value, seen)
	}
}

func isSSE(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return true
	}
	return bytes.Contains(body, []byte("\ndata:")) || bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:"))
}

func findUsage(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		if raw, ok := v["usage"]; ok {
			if usage, ok := raw.(map[string]any); ok {
				return usage, true
			}
		}
		for _, raw := range v {
			if usage, ok := findUsage(raw); ok {
				return usage, true
			}
		}
	case []any:
		for _, raw := range v {
			if usage, ok := findUsage(raw); ok {
				return usage, true
			}
		}
	}
	return nil, false
}

func collectToolCalls(value any, seen map[string]bool) []artifacts.ToolCallDetail {
	switch v := value.(type) {
	case map[string]any:
		var details []artifacts.ToolCallDetail
		if raw, ok := v["tool_calls"]; ok {
			if calls, ok := raw.([]any); ok {
				for i, call := range calls {
					key := toolCallKey(call, i)
					if key != "" && seen[key] {
						continue
					}
					if key != "" {
						seen[key] = true
					}
					details = append(details, toolCallDetail(call))
				}
			}
		}
		if isDirectToolCall(v) {
			key := toolCallKey(v, 0)
			if key == "" || !seen[key] {
				if key != "" {
					seen[key] = true
				}
				details = append(details, toolCallDetail(v))
			}
		}
		for key, raw := range v {
			if key == "tool_calls" {
				continue
			}
			details = append(details, collectToolCalls(raw, seen)...)
		}
		return details
	case []any:
		var details []artifacts.ToolCallDetail
		for _, raw := range v {
			details = append(details, collectToolCalls(raw, seen)...)
		}
		return details
	default:
		return nil
	}
}

func summarizeToolCalls(details []artifacts.ToolCallDetail) artifacts.ToolCallMetrics {
	metrics := artifacts.ToolCallMetrics{Count: len(details)}
	for _, detail := range details {
		if detail.ArgumentsValid {
			metrics.ValidCount++
		}
	}
	return metrics
}

func toolCallDetail(value any) artifacts.ToolCallDetail {
	raw, ok := value.(map[string]any)
	if !ok {
		return artifacts.ToolCallDetail{}
	}
	detail := artifacts.ToolCallDetail{
		ID:   firstString(raw, "id", "call_id", "tool_call_id"),
		Type: firstString(raw, "type"),
	}
	if fn, ok := raw["function"].(map[string]any); ok {
		detail.Name = firstString(fn, "name")
		detail.Arguments = argumentString(fn["arguments"])
	} else {
		detail.Name = firstString(raw, "name")
		detail.Arguments = argumentString(raw["arguments"])
	}
	detail.ArgumentsValid = jsonArgumentsValid(detail.Arguments)
	if detail.Arguments == "" {
		if fn, ok := raw["function"].(map[string]any); ok {
			detail.ArgumentsValid = jsonArgumentsValid(fn["arguments"])
		} else {
			detail.ArgumentsValid = jsonArgumentsValid(raw["arguments"])
		}
	}
	return detail
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok {
			return value
		}
	}
	return ""
}

func argumentString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]any, []any:
		data, err := json.Marshal(v)
		if err == nil {
			return string(data)
		}
	}
	return ""
}

func isDirectToolCall(raw map[string]any) bool {
	typ, _ := raw["type"].(string)
	switch typ {
	case "function_call", "tool_call":
		return true
	default:
		return false
	}
}

func toolCallKey(value any, fallback int) string {
	raw, ok := value.(map[string]any)
	if !ok {
		return fmt.Sprintf("tool_call:%d", fallback)
	}
	for _, key := range []string{"id", "call_id", "tool_call_id"} {
		if id, ok := raw[key].(string); ok && id != "" {
			return key + ":" + id
		}
	}
	if fn, ok := raw["function"].(map[string]any); ok {
		if name, ok := fn["name"].(string); ok && name != "" {
			return fmt.Sprintf("function:%s:%d", name, fallback)
		}
	}
	if name, ok := raw["name"].(string); ok && name != "" {
		return fmt.Sprintf("name:%s:%d", name, fallback)
	}
	return fmt.Sprintf("tool_call:%d", fallback)
}

func toolCallArgumentsValid(value any) bool {
	raw, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if args, ok := raw["arguments"]; ok {
		return jsonArgumentsValid(args)
	}
	if fn, ok := raw["function"].(map[string]any); ok {
		if args, ok := fn["arguments"]; ok {
			return jsonArgumentsValid(args)
		}
	}
	return false
}

func jsonArgumentsValid(value any) bool {
	switch v := value.(type) {
	case map[string]any, []any:
		return true
	case string:
		if strings.TrimSpace(v) == "" {
			return false
		}
		var decoded any
		return json.Unmarshal([]byte(v), &decoded) == nil
	default:
		return false
	}
}

func normalizeUsage(raw map[string]any) artifacts.Usage {
	costUSD, costKnown := floatField(raw, "cost", "cost_usd", "total_cost")
	usage := artifacts.Usage{
		InputTokens:         intField(raw, "input_tokens", "prompt_tokens", "native_tokens_prompt"),
		OutputTokens:        intField(raw, "output_tokens", "completion_tokens", "native_tokens_completion"),
		CacheCreationTokens: intField(raw, "cache_creation_tokens", "cache_write_tokens", "input_cache_write_tokens"),
		CacheReadTokens:     intField(raw, "cache_tokens", "cache_read_tokens", "input_cache_read_tokens"),
		TotalTokens:         intField(raw, "total_tokens", "native_tokens_total"),
		CostUSD:             costUSD,
		CostKnown:           costKnown,
		CostState:           artifacts.CostStateInlineConfirmed,
		Raw:                 raw,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.CacheCreationTokens + usage.CacheReadTokens
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 && usage.CostUSD == 0 {
		usage.CostState = artifacts.CostStatePending
	}
	return usage
}

func stringField(value any, key string) (string, bool) {
	m, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	raw, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	return s, ok && s != ""
}

func nestedStringField(value any, keys ...string) (string, bool) {
	current := value
	for _, key := range keys[:len(keys)-1] {
		m, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current = m[key]
	}
	return stringField(current, keys[len(keys)-1])
}

func intField(raw map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if n, ok := numberToFloat(value); ok {
				return int(n)
			}
		}
	}
	return 0
}

func floatField(raw map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if n, ok := numberToFloat(value); ok {
				return n, true
			}
		}
	}
	return 0, false
}

func numberToFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	case string:
		n, err := strconv.ParseFloat(v, 64)
		return n, err == nil
	default:
		_ = fmt.Sprintf("%v", value)
		return 0, false
	}
}
