package responses

import "router-eval/internal/workloads/probes"

func Definitions() []probes.Definition {
	return []probes.Definition{
		{Name: "simple_request", Endpoint: "/v1/responses", Description: "minimal Responses request; basic success, TTFT, and E2E latency"},
		{Name: "long_context", Endpoint: "/v1/responses", Description: "large prompt with deterministic extraction; stresses request size, context handling, and cost"},
		{Name: "streaming_responses", Endpoint: "/v1/responses", Description: "bounded longer Responses SSE request"},
		{Name: "structured_tool_call", Endpoint: "/v1/responses", Description: "single required tool call with JSON args"},
		{Name: "parallel_tool_call", Endpoint: "/v1/responses", Description: "multiple tool call shape"},
	}
}
