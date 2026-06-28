package ui

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"router-eval/internal/artifacts"
	"router-eval/internal/metrics"
	"router-eval/internal/parsers"
)

// Static UI assets are embedded in the CLI. Run data is intentionally read from
// OutDir by the /api/* handlers so measured artifacts are not baked into builds.
//
//go:embed static/*
var staticFiles embed.FS

type Server struct {
	OutDir string
	static http.Handler
}

func NewServer(outDir string) (*Server, error) {
	if outDir == "" {
		outDir = "out"
	}
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	return &Server{
		OutDir: outDir,
		static: http.FileServer(http.FS(sub)),
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/index", s.handleIndex)
	mux.HandleFunc("/api/aggregate", s.handleAggregate)
	mux.HandleFunc("/api/runs/", s.handleRun)
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

func (s *Server) handleAggregate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	index, err := s.loadIndex()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, artifacts.BuildUIAggregate(index.Runs))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	index, err := s.loadIndex()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, index)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/")
	if runID == "" || strings.Contains(runID, "/") || strings.Contains(runID, "..") {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	runDir := filepath.Join(s.OutDir, "runs", runID)
	manifest, err := readJSONFile[artifacts.Manifest](filepath.Join(runDir, "manifest.json"))
	if err != nil {
		http.Error(w, "manifest not found", http.StatusNotFound)
		return
	}
	storedMetrics, _ := readJSONFile[artifacts.Metrics](filepath.Join(runDir, "metrics.json"))
	requests, _ := s.readRunRequests(runID)
	runMetrics := storedMetrics
	if len(requests) > 0 {
		runMetrics = metrics.FromRequests(runID, requests)
		runMetrics.Context.CodexWallMillis = storedMetrics.Context.CodexWallMillis
		runMetrics.Context.CodexRouterBusyMillis = storedMetrics.Context.CodexRouterBusyMillis
	}
	prompt, _ := os.ReadFile(filepath.Join(runDir, "prompt.txt"))

	writeJSON(w, RunDetail{
		Manifest: manifest,
		Metrics:  runMetrics,
		Requests: requests,
		Prompt:   string(prompt),
	})
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		data, err := staticFiles.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "index not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	s.static.ServeHTTP(w, r)
}

func (s *Server) loadIndex() (artifacts.Index, error) {
	index := artifacts.Index{Runs: []artifacts.RunCatalog{}}
	indexPath := artifacts.RootIndexPath(s.OutDir)
	if data, err := os.ReadFile(indexPath); err == nil {
		if err := json.Unmarshal(data, &index); err != nil {
			return artifacts.Index{}, err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return artifacts.Index{}, err
	}
	scanned, err := s.scanRuns()
	if err != nil {
		return artifacts.Index{}, err
	}
	index.Runs = mergeRunCatalogs(index.Runs, scanned)
	index.Runs = s.refreshRunSummaries(index.Runs)
	sortRunCatalog(index.Runs)
	return index, nil
}

func (s *Server) scanRuns() ([]artifacts.RunCatalog, error) {
	runsDir := filepath.Join(s.OutDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if errors.Is(err, os.ErrNotExist) {
		return []artifacts.RunCatalog{}, nil
	}
	if err != nil {
		return nil, err
	}
	runs := []artifacts.RunCatalog{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDir := filepath.Join(runsDir, entry.Name())
		manifest, err := readJSONFile[artifacts.Manifest](filepath.Join(runDir, "manifest.json"))
		if err != nil {
			continue
		}
		metrics, _ := readJSONFile[artifacts.Metrics](filepath.Join(runDir, "metrics.json"))
		metrics = s.freshMetrics(entry.Name(), metrics)
		runs = append(runs, artifacts.RunCatalog{
			RunID:     manifest.RunID,
			Router:    manifest.Router,
			Model:     manifest.Model,
			Workload:  manifest.Workload,
			Status:    manifest.Status,
			StartedAt: manifest.StartedAt,
			EndedAt:   manifest.EndedAt,
			Summary: artifacts.RunCatalogSummary{
				TotalCostUSD:       metrics.Comparable.TotalCostUSD,
				TotalCostKnown:     metrics.Comparable.TotalCostKnown,
				TTFTP50Millis:      metrics.Comparable.TTFTP50Millis,
				E2EP50Millis:       metrics.Comparable.E2EP50Millis,
				E2EP95Millis:       metrics.Comparable.E2EP95Millis,
				TotalRequestMillis: metrics.Comparable.TotalRequestMillis,
				SuccessRate:        metrics.Comparable.SuccessRate,
				RequestCount:       metrics.Context.RequestCount,
				InputTokens:        metrics.Context.InputTokens,
				OutputTokens:       metrics.Context.OutputTokens,
				ToolCallCount:      metrics.Context.ToolCallCount,
			},
		})
	}
	sortRunCatalog(runs)
	return runs, nil
}

func (s *Server) refreshRunSummaries(runs []artifacts.RunCatalog) []artifacts.RunCatalog {
	refreshed := make([]artifacts.RunCatalog, 0, len(runs))
	for _, run := range runs {
		runMetrics := s.freshMetrics(run.RunID, artifacts.Metrics{})
		if runMetrics.RunID != "" {
			run.Summary = artifacts.RunCatalogSummary{
				TotalCostUSD:       runMetrics.Comparable.TotalCostUSD,
				TotalCostKnown:     runMetrics.Comparable.TotalCostKnown,
				TTFTP50Millis:      runMetrics.Comparable.TTFTP50Millis,
				E2EP50Millis:       runMetrics.Comparable.E2EP50Millis,
				E2EP95Millis:       runMetrics.Comparable.E2EP95Millis,
				TotalRequestMillis: runMetrics.Comparable.TotalRequestMillis,
				SuccessRate:        runMetrics.Comparable.SuccessRate,
				RequestCount:       runMetrics.Context.RequestCount,
				InputTokens:        runMetrics.Context.InputTokens,
				OutputTokens:       runMetrics.Context.OutputTokens,
				ToolCallCount:      runMetrics.Context.ToolCallCount,
			}
		}
		refreshed = append(refreshed, run)
	}
	return refreshed
}

func (s *Server) freshMetrics(runID string, fallback artifacts.Metrics) artifacts.Metrics {
	requests, err := s.readRunRequests(runID)
	if err != nil || len(requests) == 0 {
		return fallback
	}
	runMetrics := metrics.FromRequests(runID, requests)
	runMetrics.Context.CodexWallMillis = fallback.Context.CodexWallMillis
	runMetrics.Context.CodexRouterBusyMillis = fallback.Context.CodexRouterBusyMillis
	return runMetrics
}

func mergeRunCatalogs(indexed []artifacts.RunCatalog, scanned []artifacts.RunCatalog) []artifacts.RunCatalog {
	byID := map[string]artifacts.RunCatalog{}
	for _, run := range indexed {
		if run.RunID != "" {
			byID[run.RunID] = run
		}
	}
	for _, run := range scanned {
		if run.RunID == "" {
			continue
		}
		if _, exists := byID[run.RunID]; !exists {
			byID[run.RunID] = run
			continue
		}
		// Prefer scanned metadata when the index is stale or incomplete.
		indexedRun := byID[run.RunID]
		if indexedRun.Workload.Kind == "" || indexedRun.Summary.RequestCount == 0 {
			byID[run.RunID] = run
		}
	}
	runs := make([]artifacts.RunCatalog, 0, len(byID))
	for _, run := range byID {
		runs = append(runs, run)
	}
	return runs
}

type RunDetail struct {
	Manifest artifacts.Manifest        `json:"manifest"`
	Metrics  artifacts.Metrics         `json:"metrics"`
	Requests []artifacts.RequestRecord `json:"requests"`
	Prompt   string                    `json:"prompt,omitempty"`
}

func (s *Server) readRunRequests(runID string) ([]artifacts.RequestRecord, error) {
	requests, err := readRequests(filepath.Join(s.OutDir, "runs", runID, "requests.jsonl"))
	if err != nil {
		return nil, err
	}
	for i := range requests {
		deriveMissingTiming(&requests[i])
		deriveMissingStatus(&requests[i])
		s.enrichRequestFromRaw(&requests[i])
	}
	return requests, nil
}

func (s *Server) enrichRequestFromRaw(record *artifacts.RequestRecord) {
	if record == nil || record.Diagnostics.RawCapturePath == "" {
		return
	}
	rawPath := s.resolveRawCapturePath(record.Diagnostics.RawCapturePath)
	body, err := os.ReadFile(rawPath)
	if err != nil || len(body) == 0 {
		return
	}
	contentType := firstHeader(record.Diagnostics.Headers, "Content-Type")
	parsed := parsers.Parse(record.Endpoint, contentType, body)
	if parsed.ID != "" {
		record.RequestID = parsed.ID
	}
	if parsed.GenerationID != "" {
		record.GenerationID = parsed.GenerationID
	}
	if parsed.ToolCalls.Count == 0 {
		return
	}
	record.Context.ToolCallCount = parsed.ToolCalls.Count
	record.Context.ValidToolCallCount = parsed.ToolCalls.ValidCount
	record.Comparable.ToolCallValidityRate = toolCallValidityRate(parsed.ToolCalls)
	record.Diagnostics.ToolCalls = parsed.ToolDetails
}

func (s *Server) resolveRawCapturePath(rawPath string) string {
	if rawPath == "" || filepath.IsAbs(rawPath) {
		return rawPath
	}
	if _, err := os.Stat(rawPath); err == nil {
		return rawPath
	}
	return filepath.Join(s.OutDir, strings.TrimPrefix(rawPath, strings.TrimPrefix(s.OutDir, string(os.PathSeparator))))
}

func firstHeader(headers map[string][]string, key string) string {
	for name, values := range headers {
		if strings.EqualFold(name, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func deriveMissingTiming(record *artifacts.RequestRecord) {
	if record == nil || record.Timing.E2EMillis > 0 {
		return
	}
	if record.Timing.RequestSentUnixNano == 0 || record.Timing.LastByteUnixNano == 0 {
		return
	}
	record.Timing.E2EMillis = float64(record.Timing.LastByteUnixNano-record.Timing.RequestSentUnixNano) / 1_000_000
	record.Comparable.E2EMillis = record.Timing.E2EMillis
}

func deriveMissingStatus(record *artifacts.RequestRecord) {
	if record == nil || record.StatusCode != 0 {
		return
	}
	if (record.ErrorClass != "stream" && record.ErrorClass != "client_cancel") || record.Error != "context canceled" {
		return
	}
	if record.Timing.FirstByteUnixNano == 0 || record.Context.ResponseBytes == 0 {
		return
	}
	record.StatusCode = http.StatusOK
	record.Success = true
	record.ErrorClass = "client_cancel"
}

func toolCallValidityRate(metrics artifacts.ToolCallMetrics) float64 {
	if metrics.Count == 0 {
		return 0
	}
	return float64(metrics.ValidCount) / float64(metrics.Count)
}

func readJSONFile[T any](path string) (T, error) {
	var value T
	data, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(data, &value)
	return value, err
}

func readRequests(path string) ([]artifacts.RequestRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	requests := make([]artifacts.RequestRecord, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record artifacts.RequestRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		requests = append(requests, record)
	}
	return requests, nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}

func sortRunCatalog(runs []artifacts.RunCatalog) {
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
}
