package openrouter

import (
	"fmt"
	"net/http"
	"os"
)

type Adapter struct {
	apiKey string
}

func New() *Adapter {
	return &Adapter{apiKey: os.Getenv("OPENROUTER_API_KEY")}
}

func (a *Adapter) Name() string {
	return "openrouter"
}

func (a *Adapter) InjectAuth(req *http.Request) error {
	if a.apiKey == "" {
		return fmt.Errorf("OPENROUTER_API_KEY is not set")
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	return nil
}

func (a *Adapter) CaptureIDs(headers http.Header) (requestID, generationID string, routerFields map[string]any) {
	generationID = headers.Get("X-Generation-Id")
	fields := map[string]any{}
	if generationID != "" {
		fields["x_generation_id"] = generationID
	}
	return generationID, generationID, fields
}
