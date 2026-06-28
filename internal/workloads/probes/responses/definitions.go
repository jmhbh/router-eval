package responses

import "router-eval/internal/workloads/probes"

func Definitions() []probes.Definition {
	return []probes.Definition{
		{Name: "simple_request", Endpoint: "/v1/responses", Description: "minimal non-streaming Responses request; router-tax floor for E2E latency and cost"},
		{Name: "streaming_responses", Endpoint: "/v1/responses", Description: "streaming request with a length-pinned output; TTFT and tokens/sec"},
	}
}
