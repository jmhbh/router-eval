package responses

import (
	"fmt"
	"strings"

	"router-eval/internal/workloads/probes"
)

func Request(name, model string) (probes.Request, error) {
	if model == "" {
		return probes.Request{}, fmt.Errorf("model is required")
	}
	switch name {
	case "simple_request":
		return probes.Request{
			Name:     name,
			Endpoint: "/v1/responses",
			Payload: map[string]any{
				"model":  model,
				"input":  "Reply with exactly one short sentence about Responses API compatibility.",
				"stream": false,
			},
		}, nil
	case "long_context":
		return probes.Request{
			Name:     name,
			Endpoint: "/v1/responses",
			Payload: map[string]any{
				"model":  model,
				"input":  longContextInput(),
				"stream": false,
			},
		}, nil
	case "streaming_responses":
		return probes.Request{
			Name:     name,
			Endpoint: "/v1/responses",
			Stream:   true,
			Payload: map[string]any{
				"model":  model,
				"input":  "Write 8 concise bullet points about streamed response measurement.",
				"stream": true,
			},
		}, nil
	case "structured_tool_call":
		return probes.Request{
			Name:     name,
			Endpoint: "/v1/responses",
			Payload: map[string]any{
				"model": model,
				"input": "Call the record_latency tool with ttft_ms=123 and e2e_ms=456.",
				"tools": []map[string]any{recordLatencyTool()},
				"tool_choice": map[string]any{
					"type": "function",
					"name": "record_latency",
				},
				"stream": true,
			},
		}, nil
	case "parallel_tool_call":
		return probes.Request{
			Name:     name,
			Endpoint: "/v1/responses",
			Payload: map[string]any{
				"model":               model,
				"input":               "Call both record_latency and record_cost with plausible numeric values.",
				"tools":               []map[string]any{recordLatencyTool(), recordCostTool()},
				"parallel_tool_calls": true,
				"stream":              true,
			},
		}, nil
	default:
		return probes.Request{}, fmt.Errorf("unknown Responses probe %q", name)
	}
}

// longContextInput builds a large, deterministic prompt with a single needle so
// the expected answer is objectively checkable while the request stresses request
// size, context handling, and the cost path.
func longContextInput() string {
	var b strings.Builder
	b.WriteString("You are given a long document. Find the line containing MAGIC_TOKEN and reply with only the number that follows it.\n\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "line %d: the quick brown fox jumps over the lazy dog; filler filler filler filler\n", i)
		if i == 237 {
			b.WriteString("MAGIC_TOKEN: 8675309\n")
		}
	}
	b.WriteString("\nWhat number follows MAGIC_TOKEN? Reply with just the number.")
	return b.String()
}

func recordLatencyTool() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "record_latency",
		"description": "Record latency measurements.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ttft_ms": map[string]any{"type": "number"},
				"e2e_ms":  map[string]any{"type": "number"},
			},
			"required": []string{"ttft_ms", "e2e_ms"},
		},
	}
}

func recordCostTool() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "record_cost",
		"description": "Record request cost.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cost_usd": map[string]any{"type": "number"},
			},
			"required": []string{"cost_usd"},
		},
	}
}
