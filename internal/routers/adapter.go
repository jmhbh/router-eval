package routers

import (
	"fmt"
	"net/http"
	"strings"

	"router-eval/internal/routers/openrouter"
	"router-eval/internal/routers/tokenrouter"
)

type Adapter interface {
	Name() string
	InjectAuth(req *http.Request) error
	CaptureIDs(headers http.Header) (requestID, generationID string, routerFields map[string]any)
}

func NewAdapter(name string) (Adapter, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "tokenrouter", "token-router":
		return tokenrouter.New(), nil
	case "openrouter", "open-router":
		return openrouter.New(), nil
	default:
		return nil, fmt.Errorf("unknown router adapter %q", name)
	}
}
