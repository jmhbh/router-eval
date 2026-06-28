package usage

import "router-eval/internal/artifacts"

type LookupResult struct {
	RequestID    string
	GenerationID string
	Usage        artifacts.Usage
	Raw          any
	Found        bool
}
