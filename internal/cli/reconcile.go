package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"router-eval/internal/artifacts"
	"router-eval/internal/metrics"
	"router-eval/internal/routers/openrouter"
	"router-eval/internal/routers/tokenrouter"
)

type reconcileOptions struct {
	runID   string
	outDir  string
	csvPath string
	timeout time.Duration
}

func newReconcileCommand() *cobra.Command {
	opts := reconcileOptions{}
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile an existing run's usage and cost artifacts",
		Example: stringsJoin([]string{
			"router-eval reconcile --run-id 20260627T233155Z --out out",
			"router-eval reconcile --run-id 20260627T233155Z --out out --csv usage_logs.csv",
			"router-eval reconcile --run-id 20260628T042350Z --out out --csv openrouter_activity.csv",
		}, "\n"),
		RunE: func(_ *cobra.Command, _ []string) error {
			return runReconcile(opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.runID, "run-id", "", "existing run id to reconcile")
	flags.StringVar(&opts.outDir, "out", "out", "artifact root directory")
	flags.StringVar(&opts.csvPath, "csv", "", "local usage CSV export to reconcile from: tokenRouter usage export, or OpenRouter activity export (download manually; required for OpenRouter cost since the wire generation id is not billable)")
	flags.DurationVar(&opts.timeout, "timeout", 45*time.Second, "reconciliation timeout")
	return cmd
}

func runReconcile(opts reconcileOptions) error {
	if opts.runID == "" {
		return errors.New("--run-id is required")
	}
	store, err := artifacts.NewStore(opts.outDir, opts.runID)
	if err != nil {
		return err
	}
	manifest, err := readArtifactJSON[artifacts.Manifest](store.ManifestPath())
	if err != nil {
		return err
	}
	existingMetrics, _ := readArtifactJSON[artifacts.Metrics](store.MetricsPath())
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	var records []artifacts.RequestRecord
	if opts.csvPath != "" {
		records, err = reconcileFromCSV(store, manifest, opts.csvPath)
	} else {
		records, err = maybeReconcileUsage(ctx, store, manifest)
	}
	if err != nil {
		return err
	}
	runMetrics := metrics.FromRequests(opts.runID, records)
	runMetrics.Context.CodexWallMillis = existingMetrics.Context.CodexWallMillis
	runMetrics.Context.CodexRouterBusyMillis = existingMetrics.Context.CodexRouterBusyMillis
	if err := store.WriteMetrics(runMetrics); err != nil {
		return err
	}
	if err := store.UpdateIndex(manifest, runMetrics); err != nil {
		return err
	}
	fmt.Printf("reconciled run_id=%s requests=%d cost_known=%t total_cost_usd=%.6f\n",
		opts.runID,
		len(records),
		runMetrics.Comparable.TotalCostKnown,
		runMetrics.Comparable.TotalCostUSD,
	)
	return nil
}

func reconcileFromCSV(store *artifacts.Store, manifest artifacts.Manifest, csvPath string) ([]artifacts.RequestRecord, error) {
	records, err := store.ReadRequests()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(csvPath)
	if err != nil {
		return nil, err
	}
	switch manifest.Router {
	case "tokenrouter":
		rows, headers, err := tokenrouter.ParseUsageCSV(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		return reconcileTokenRouterUsageFromExport(store, manifest, records, tokenrouter.UsageExport{
			Rows:    rows,
			Headers: headers,
			RawCSV:  raw,
			URL:     csvPath,
			Window:  tokenrouter.TimeWindow{},
		})
	case "openrouter":
		rows, headers, err := openrouter.ParseActivityCSV(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		return reconcileOpenRouterActivityFromExport(store, manifest, records, rows, headers, raw, csvPath)
	default:
		return nil, fmt.Errorf("--csv reconciliation is supported for tokenrouter and openrouter runs")
	}
}

func readArtifactJSON[T any](path string) (T, error) {
	var value T
	data, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(data, &value)
	return value, err
}
