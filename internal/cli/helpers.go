package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"router-eval/internal/artifacts"
	"router-eval/internal/proxy"
	"router-eval/internal/routers"
	"router-eval/internal/routers/openrouter"
	"router-eval/internal/routers/tokenrouter"
)

type runningProxy struct {
	server *proxy.Server
	errCh  <-chan error
}

func startMeasurementProxy(addr, upstream, outDir, runID string, adapter routers.Adapter, timeout time.Duration, proxyKey string) (*runningProxy, net.Listener, error) {
	server, err := proxy.NewServer(proxy.Config{
		Addr:        addr,
		Upstream:    upstream,
		OutDir:      outDir,
		RunID:       runID,
		Router:      adapter,
		HTTPClient:  &http.Client{Timeout: timeout},
		ProxyAPIKey: proxyKey,
	})
	if err != nil {
		return nil, nil, err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	return &runningProxy{server: server, errCh: errCh}, listener, nil
}

func stopMeasurementProxy(running *runningProxy) error {
	if running == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownErr := running.server.Shutdown(shutdownCtx)
	cancel()
	serveErr := <-running.errCh
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return shutdownErr
}

func finalizeProbeManifest(store *artifacts.Store, manifest artifacts.Manifest, status artifacts.RunStatus, cause error) error {
	_, err := finalizedManifest(store, manifest, status, cause)
	return err
}

func finalizedManifest(store *artifacts.Store, manifest artifacts.Manifest, status artifacts.RunStatus, cause error) (artifacts.Manifest, error) {
	now := time.Now().UTC()
	manifest.Status = status
	manifest.EndedAt = &now
	if cause != nil {
		if manifest.Config == nil {
			manifest.Config = map[string]string{}
		}
		manifest.Config["error"] = cause.Error()
	}
	if err := store.WriteManifest(manifest); err != nil {
		if cause != nil {
			return manifest, fmt.Errorf("%w; additionally failed to write manifest: %v", cause, err)
		}
		return manifest, err
	}
	return manifest, cause
}

func maybeReconcileUsage(ctx context.Context, store *artifacts.Store, manifest artifacts.Manifest) ([]artifacts.RequestRecord, error) {
	records, err := store.ReadRequests()
	if err != nil {
		return nil, err
	}
	switch manifest.Router {
	case "tokenrouter":
		return reconcileTokenRouterUsage(ctx, store, manifest, records)
	case "openrouter":
		// OpenRouter cost is reconciled out-of-band from a downloaded activity CSV
		// (`reconcile --csv`); the wire generation id is not billable for Codex
		// traffic, so there is no live lookup during the run. Inline usage captured
		// from the response stream is the run-time cost signal.
		return records, nil
	default:
		return records, nil
	}
}

// reconcileTokenRouterUsage reconciles cost metrics
// by matching requestId against usage logs csv procured from  management API
func reconcileTokenRouterUsage(ctx context.Context, store *artifacts.Store, manifest artifacts.Manifest, records []artifacts.RequestRecord) ([]artifacts.RequestRecord, error) {
	upstream := ""
	if manifest.Config != nil {
		upstream = manifest.Config["upstream"]
	}
	client := tokenrouter.NewUsageClient(upstream)
	export, err := client.Export(ctx, tokenRouterWindow(manifest, 10*time.Second))
	if err != nil {
		return records, err
	}
	return reconcileTokenRouterUsageFromExport(store, manifest, records, export)
}

func reconcileTokenRouterUsageFromExport(store *artifacts.Store, manifest artifacts.Manifest, records []artifacts.RequestRecord, export tokenrouter.UsageExport) ([]artifacts.RequestRecord, error) {
	filteredRows := filterTokenRouterRows(manifest, export.Rows)
	export.Rows = filteredRows
	byRequestID := map[string]tokenrouter.UsageRow{}
	for _, row := range export.Rows {
		byRequestID[row.RequestID] = row
	}
	var matched int
	for i := range records {
		row, matchedID, ok := matchTokenRouterRow(records[i], byRequestID)
		if !ok {
			continue
		}
		result := row.LookupResult()
		records[i].Usage = result.Usage
		records[i].Comparable.CostUSD = result.Usage.CostUSD
		records[i].Context.InputTokens = result.Usage.InputTokens
		records[i].Context.OutputTokens = result.Usage.OutputTokens
		if records[i].Diagnostics.RouterSpecific == nil {
			records[i].Diagnostics.RouterSpecific = map[string]any{}
		}
		records[i].Diagnostics.RouterSpecific["usage_reconciled"] = true
		records[i].Diagnostics.RouterSpecific["usage_reconciliation_source"] = "tokenrouter_csv_export"
		records[i].Diagnostics.RouterSpecific["usage_reconciliation_request_id"] = matchedID
		matched++
	}
	if err := writeTokenRouterReconciliationArtifacts(store, records, export); err != nil {
		return records, err
	}
	if matched == 0 {
		return records, nil
	}
	return records, store.WriteRequests(records)
}

func filterTokenRouterRows(manifest artifacts.Manifest, rows []tokenrouter.UsageRow) []tokenrouter.UsageRow {
	if manifest.Model == "" {
		return rows
	}
	filtered := make([]tokenrouter.UsageRow, 0, len(rows))
	for _, row := range rows {
		if row.ModelName == "" || row.ModelName == manifest.Model {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func matchTokenRouterRow(record artifacts.RequestRecord, rows map[string]tokenrouter.UsageRow) (tokenrouter.UsageRow, string, bool) {
	for _, id := range tokenRouterCandidateIDs(record) {
		row, ok := rows[id]
		if ok {
			return row, id, true
		}
	}
	return tokenrouter.UsageRow{}, "", false
}

func tokenRouterCandidateIDs(record artifacts.RequestRecord) []string {
	var ids []string
	for _, id := range []string{record.RequestID, record.GenerationID} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if value, ok := record.Diagnostics.RouterSpecific["x_tokenrouter_request_id"]; ok {
		if id, ok := value.(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func tokenRouterWindow(manifest artifacts.Manifest, wiggle time.Duration) tokenrouter.TimeWindow {
	start := manifest.StartedAt.Add(-wiggle)
	end := time.Now().UTC().Add(wiggle)
	if manifest.EndedAt != nil {
		end = manifest.EndedAt.Add(wiggle)
	}
	return tokenrouter.TimeWindow{
		StartUnix: start.Unix(),
		EndUnix:   end.Unix(),
	}
}

func writeTokenRouterReconciliationArtifacts(store *artifacts.Store, records []artifacts.RequestRecord, export tokenrouter.UsageExport) error {
	dir := filepath.Join(store.RunDir(), "reconciliation")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "tokenrouter_usage_export.csv"), export.RawCSV, 0o644); err != nil {
		return err
	}
	rowIDs := map[string]bool{}
	for _, row := range export.Rows {
		rowIDs[row.RequestID] = true
	}
	var matched []string
	var unmatched []string
	for _, record := range records {
		_, matchedID, ok := matchTokenRouterRow(record, rowIDsToEmptyRows(rowIDs))
		if ok {
			matched = append(matched, matchedID)
		} else {
			unmatched = append(unmatched, firstNonEmpty(tokenRouterCandidateIDs(record)...))
		}
	}
	summary := map[string]any{
		"source":                  "tokenrouter_csv_export",
		"url":                     export.URL,
		"headers":                 export.Headers,
		"window":                  export.Window,
		"export_row_count":        len(export.Rows),
		"model_filter":            "",
		"local_request_count":     len(records),
		"matched_request_count":   len(matched),
		"unmatched_request_count": len(unmatched),
		"matched_request_ids":     matched,
		"unmatched_request_ids":   unmatched,
	}
	if len(export.Rows) > 0 {
		summary["model_filter"] = export.Rows[0].ModelName
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, "summary.json"), data, 0o644)
}

func rowIDsToEmptyRows(ids map[string]bool) map[string]tokenrouter.UsageRow {
	rows := map[string]tokenrouter.UsageRow{}
	for id := range ids {
		rows[id] = tokenrouter.UsageRow{RequestID: id}
	}
	return rows
}

// reconcileOpenRouterActivityFromExport reconciles OpenRouter cost from a manually
// downloaded activity CSV. The wire generation id is not billable for Codex
// traffic, so each captured record is matched to its billable activity row by the
// generation id's embedded second (±1s) + model + token-count proximity. The
// billable id becomes the record's generation id and the wire id is kept as a
// diagnostic.
func reconcileOpenRouterActivityFromExport(store *artifacts.Store, manifest artifacts.Manifest, records []artifacts.RequestRecord, rows []openrouter.ActivityRow, headers []string, rawCSV []byte, source string) ([]artifacts.RequestRecord, error) {
	matches := openrouter.MatchActivityRows(records, rows, manifest.Model)
	matchedIdx := map[int]bool{}
	matchedSummaries := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		i := m.RecordIndex
		wireID := firstNonEmpty(records[i].GenerationID, records[i].RequestID)
		result := m.Row.LookupResult()
		records[i].Usage = result.Usage
		records[i].Comparable.CostUSD = result.Usage.CostUSD
		records[i].Context.InputTokens = result.Usage.InputTokens
		records[i].Context.OutputTokens = result.Usage.OutputTokens
		records[i].GenerationID = m.Row.GenerationID // billable id becomes the record id
		setRouterField(&records[i], "usage_reconciled", true)
		setRouterField(&records[i], "usage_reconciliation_source", "openrouter_activity_csv")
		setRouterField(&records[i], "usage_reconciliation_strategy", m.Strategy)
		setRouterField(&records[i], "billable_generation_id", m.Row.GenerationID)
		setRouterField(&records[i], "wire_generation_id", wireID)
		setRouterField(&records[i], "usage_reconciliation_seconds_off", m.SecondsOff)
		setRouterField(&records[i], "usage_reconciliation_token_distance", m.TokenDistance)
		matchedIdx[i] = true
		matchedSummaries = append(matchedSummaries, map[string]any{
			"wire_generation_id":     wireID,
			"billable_generation_id": m.Row.GenerationID,
			"created_second":         m.Second,
			"seconds_off":            m.SecondsOff,
			"token_distance":         m.TokenDistance,
			"strategy":               m.Strategy,
			"cost_usd":               m.Row.CostTotal,
		})
	}

	// Unmatched records keep their inline usage (if any) and are flagged: without an
	// activity-row match, inline usage is the best available cost signal.
	var inlineOnly []string
	for i := range records {
		if matchedIdx[i] {
			continue
		}
		if recordHasInlineUsage(records[i]) {
			setRouterField(&records[i], "usage_api_unavailable", true)
			if records[i].Usage.CostState == "" || records[i].Usage.CostState == artifacts.CostStatePending {
				records[i].Usage.CostState = artifacts.CostStateInlineConfirmed
			}
			inlineOnly = append(inlineOnly, firstNonEmpty(records[i].GenerationID, records[i].RequestID))
		}
	}

	summary := map[string]any{
		"source":                    source,
		"router":                    "openrouter",
		"match_strategy":            "generation_second(+/-1s)+model+tokens",
		"note":                      "OpenRouter does not bill Codex /v1/responses under the wire generation id; matched to the billable activity row by embedded second + model + token counts.",
		"headers":                   headers,
		"activity_row_count":        len(rows),
		"local_request_count":       len(records),
		"matched_request_count":     len(matches),
		"inline_only_request_count": len(inlineOnly),
		"matched":                   matchedSummaries,
		"inline_only_wire_ids":      inlineOnly,
	}
	if err := writeOpenRouterReconciliationArtifacts(store, rawCSV, summary); err != nil {
		return records, err
	}
	return records, store.WriteRequests(records)
}

func writeOpenRouterReconciliationArtifacts(store *artifacts.Store, rawCSV []byte, summary map[string]any) error {
	dir := filepath.Join(store.RunDir(), "reconciliation")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if len(rawCSV) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "openrouter_activity_export.csv"), rawCSV, 0o644); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, "summary.json"), data, 0o644)
}

func setRouterField(record *artifacts.RequestRecord, key string, value any) {
	if record.Diagnostics.RouterSpecific == nil {
		record.Diagnostics.RouterSpecific = map[string]any{}
	}
	record.Diagnostics.RouterSpecific[key] = value
}

func recordHasInlineUsage(record artifacts.RequestRecord) bool {
	u := record.Usage
	return u.CostState == artifacts.CostStateInlineConfirmed || u.CostKnown || u.CostUSD != 0 || u.InputTokens > 0 || u.OutputTokens > 0
}

func defaultUIPIDFilePath() string {
	return filepath.Join(os.TempDir(), "router-eval-ui.pid")
}

func writePIDFile(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid file %s: %w", path, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d in %s", pid, path)
	}
	return pid, nil
}

func processAlreadyFinished(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "process already finished")
}

func startRequestProgressLogger(ctx context.Context, store *artifacts.Store, interval time.Duration) func() {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	progressCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var seen int
		for {
			select {
			case <-progressCtx.Done():
				logRequestProgress(store, &seen)
				return
			case <-ticker.C:
				logRequestProgress(store, &seen)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func logRequestProgress(store *artifacts.Store, seen *int) {
	data, err := os.ReadFile(store.RequestsPath())
	if err != nil || len(data) == 0 {
		return
	}
	text := string(data)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if !strings.HasSuffix(text, "\n") && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > *seen {
		line := strings.TrimSpace(lines[*seen])
		(*seen)++
		if line == "" {
			continue
		}
		var record artifacts.RequestRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		log.Printf(
			"request progress: count=%d status=%d success=%t e2e=%s cost_state=%s input_tokens=%d output_tokens=%d",
			*seen,
			record.StatusCode,
			record.Success,
			formatDurationMillis(record.Timing.E2EMillis),
			record.Usage.CostState,
			record.Context.InputTokens,
			record.Context.OutputTokens,
		)
	}
}

func formatDurationMillis(value float64) string {
	if value >= 1000 {
		return fmt.Sprintf("%.2fs", value/1000)
	}
	return fmt.Sprintf("%.1fms", value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
