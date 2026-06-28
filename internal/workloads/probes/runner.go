package probes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Request struct {
	Name     string
	Endpoint string
	Payload  any
	Stream   bool
}

type Result struct {
	Name       string
	StatusCode int
	BodyBytes  int
	Duration   time.Duration
}

type Runner struct {
	ProxyBaseURL string
	HTTPClient   *http.Client
	APIKey       string
}

func (r Runner) Run(ctx context.Context, request Request) (Result, error) {
	if r.ProxyBaseURL == "" {
		return Result{}, fmt.Errorf("proxy base URL is required")
	}
	if request.Endpoint == "" {
		return Result{}, fmt.Errorf("probe endpoint is required")
	}
	body, err := json.Marshal(request.Payload)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(r.ProxyBaseURL, request.Endpoint), bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Name:       request.Name,
		StatusCode: resp.StatusCode,
		BodyBytes:  int(n),
		Duration:   time.Since(start),
	}, nil
}

func joinURL(base, endpoint string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	basePath := strings.TrimRight(u.Path, "/")
	reqPath := "/" + strings.TrimLeft(endpoint, "/")
	u.Path = path.Join(basePath, reqPath)
	return u.String()
}
