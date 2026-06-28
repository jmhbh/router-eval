package tokenrouter

import (
	"fmt"
	"net/http"
	"os"
)

type Adapter struct {
	apiKey string
}

func New() *Adapter {
	return &Adapter{apiKey: os.Getenv("TOKENROUTER_API_KEY")}
}

func (a *Adapter) Name() string {
	return "tokenrouter"
}

func (a *Adapter) InjectAuth(req *http.Request) error {
	if a.apiKey == "" {
		return fmt.Errorf("TOKENROUTER_API_KEY is not set")
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	return nil
}

func (a *Adapter) CaptureIDs(headers http.Header) (requestID, generationID string, routerFields map[string]any) {
	requestID = headers.Get("X-Tokenrouter-Request-Id")
	fields := map[string]any{}
	if requestID != "" {
		fields["x_tokenrouter_request_id"] = requestID
	}
	return requestID, "", fields
}
