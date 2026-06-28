package responses

import (
	"fmt"

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
	case "streaming_responses":
		return probes.Request{
			Name:     name,
			Endpoint: "/v1/responses",
			Stream:   true,
			Payload: map[string]any{
				"model":             model,
				"input":             streamingThroughputInput(),
				"stream":            true,
				"max_output_tokens": 512,
			},
		}, nil
	default:
		return probes.Request{}, fmt.Errorf("unknown Responses probe %q", name)
	}
}

// streamingThroughputInput keeps the input small (so TTFT reflects router connect +
// first-token latency, not prefill) while asking for far more output than the probe's
// max_output_tokens cap allows. The cap therefore binds on every run, so each run emits
// the same number of output tokens and tokens/sec is measured over a stable, comparable
// streaming window instead of a short, model-discretionary one.
func streamingThroughputInput() string {
	return "Write a detailed, continuous technical explanation of at least 900 words " +
		"describing how large language model APIs stream responses token by token over " +
		"server-sent events: connection setup, chunked transfer encoding, per-token " +
		"cadence, client-side buffering, and backpressure. Use flowing prose in full " +
		"paragraphs with no lists or headings, and keep writing until every aspect is " +
		"thoroughly covered."
}
