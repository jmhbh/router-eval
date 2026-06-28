package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"router-eval/internal/artifacts"
	"router-eval/internal/metrics"
	"router-eval/internal/routers"
	codexharness "router-eval/internal/workloads/harness/codex"
)

type codexOptions struct {
	addr         string
	routerName   string
	upstream     string
	runID        string
	outDir       string
	timeout      time.Duration
	codexTimeout time.Duration
	model        string
	task         string
	prompt       string
	workdir      string
	sandbox      string
	approval     string
	apiKey       string
	samples      int
}

func newCodexCommand() *cobra.Command {
	opts := codexOptions{}
	cmd := &cobra.Command{
		Use:   "codex",
		Short: "Run a Codex harness task through the local proxy",
		Example: stringsJoin([]string{
			"router-eval codex --router openrouter --upstream https://openrouter.ai/api --model MODEL --task issue-123 --prompt \"...\"",
			"router-eval codex --router tokenrouter --upstream https://api.tokenrouter.com --model MODEL --task smoke --workdir . --sandbox workspace-write --approval never --prompt \"...\"",
		}, "\n"),
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCodex(opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.addr, "addr", "127.0.0.1:0", "local proxy listen address")
	flags.StringVar(&opts.routerName, "router", "", "router adapter name: tokenrouter or openrouter")
	flags.StringVar(&opts.upstream, "upstream", "", "upstream base URL, for example https://api.tokenrouter.com")
	flags.StringVar(&opts.runID, "run-id", "", "run id for artifact output")
	flags.StringVar(&opts.outDir, "out", "out", "artifact root directory")
	flags.DurationVar(&opts.timeout, "timeout", 180*time.Second, "upstream request timeout")
	flags.DurationVar(&opts.codexTimeout, "codex-timeout", 5*time.Minute, "Codex subprocess timeout")
	flags.StringVar(&opts.model, "model", "", "model id to pass through the router")
	flags.StringVar(&opts.task, "task", "codex-task", "task name for grouping harness results")
	flags.StringVar(&opts.prompt, "prompt", "", "prompt to pass to codex exec")
	flags.StringVar(&opts.workdir, "workdir", ".", "working directory for codex")
	flags.StringVar(&opts.sandbox, "sandbox", "read-only", "Codex sandbox mode")
	flags.StringVar(&opts.approval, "approval", "never", "Codex approval policy: untrusted, on-request, or never")
	flags.StringVar(&opts.apiKey, "proxy-key", "local-proxy-key", "throwaway downstream proxy API key")
	flags.IntVar(&opts.samples, "samples", 1, "number of times to run the task; each sample is a separate run, isolated in its own workdir, aggregated by task in the UI (codex runs are expensive — start small)")

	return cmd
}

type codexSampleResult struct {
	runID     string
	runDir    string
	requests  int
	wallMs    float64
	costUSD   float64
	costKnown bool
}

func runCodex(opts codexOptions) error {
	if opts.routerName == "" {
		return errors.New("--router is required")
	}
	if opts.upstream == "" {
		return errors.New("--upstream is required")
	}
	if opts.model == "" {
		return errors.New("--model is required")
	}
	if opts.prompt == "" {
		return errors.New("--prompt is required")
	}

	samples := opts.samples
	if samples < 1 {
		samples = 1
	}
	base := opts.runID
	if base == "" {
		base = time.Now().UTC().Format("20060102T150405Z")
	}

	var results []codexSampleResult
	for i := 0; i < samples; i++ {
		runID := base
		workdir := opts.workdir
		// Each sample is isolated in its own run-id and workdir so a prior sample's
		// output file does not change the task the next sample sees.
		if samples > 1 {
			runID = fmt.Sprintf("%s-%02d", base, i+1)
			workdir = filepath.Join(opts.workdir, runID)
			if err := os.MkdirAll(workdir, 0o755); err != nil {
				return err
			}
		}
		log.Printf("codex sample %d/%d: run_id=%s", i+1, samples, runID)
		res, err := runCodexOnce(opts, runID, workdir)
		if err != nil {
			log.Printf("codex sample %d/%d failed: %v", i+1, samples, err)
			continue
		}
		results = append(results, res)
	}
	if len(results) == 0 {
		return errors.New("all codex samples failed")
	}

	walls := make([]float64, 0, len(results))
	var totalCost float64
	costKnown := true
	for _, r := range results {
		walls = append(walls, r.wallMs)
		totalCost += r.costUSD
		if !r.costKnown {
			costKnown = false
		}
	}
	fmt.Printf("codex task=%s samples=%d succeeded=%d wall_ms_median=%.1f cost_total_usd=%.6f cost_known=%t\n",
		opts.task, samples, len(results), medianFloat(walls), totalCost, costKnown)
	for _, r := range results {
		fmt.Printf("  run_id=%s requests=%d wall_ms=%.1f cost_usd=%.6f\n", r.runID, r.requests, r.wallMs, r.costUSD)
	}
	return nil
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func runCodexOnce(opts codexOptions, runID, workdir string) (codexSampleResult, error) {
	adapter, err := routers.NewAdapter(opts.routerName)
	if err != nil {
		return codexSampleResult{}, err
	}
	store, err := artifacts.NewStore(opts.outDir, runID)
	if err != nil {
		return codexSampleResult{}, err
	}
	manifest := artifacts.Manifest{
		RunID:     runID,
		Router:    adapter.Name(),
		Model:     opts.model,
		Workload:  artifacts.WorkloadRef{Kind: "harness/codex", Name: opts.task},
		Status:    artifacts.RunStatusRunning,
		StartedAt: time.Now().UTC(),
		Config: map[string]string{
			"upstream": opts.upstream,
			"workdir":  workdir,
			"sandbox":  opts.sandbox,
			"approval": opts.approval,
		},
	}
	if err := store.WriteManifest(manifest); err != nil {
		return codexSampleResult{}, err
	}
	promptPath := filepath.Join(store.RunDir(), "prompt.txt")
	if err := os.MkdirAll(store.RunDir(), 0o755); err != nil {
		return codexSampleResult{}, err
	}
	if err := os.WriteFile(promptPath, []byte(opts.prompt), 0o600); err != nil {
		return codexSampleResult{}, err
	}
	manifest.Config["prompt_path"] = promptPath
	if err := store.WriteManifest(manifest); err != nil {
		return codexSampleResult{}, err
	}

	server, listener, err := startMeasurementProxy(opts.addr, opts.upstream, opts.outDir, runID, adapter, opts.timeout, opts.apiKey)
	if err != nil {
		return codexSampleResult{}, finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, err)
	}
	proxyURL := "http://" + listener.Addr().String() + "/v1"
	log.Printf("proxy started: %s -> %s", proxyURL, opts.upstream)

	log.Printf("codex exec started")
	stopProgress := startRequestProgressLogger(context.Background(), store, 2*time.Second)
	result, runErr := codexharness.Run(context.Background(), codexharness.Config{
		Model:       opts.model,
		Prompt:      opts.prompt,
		Workdir:     workdir,
		ProxyURL:    proxyURL,
		ProxyAPIKey: opts.apiKey,
		Timeout:     opts.codexTimeout,
		Sandbox:     opts.sandbox,
		Approval:    opts.approval,
		OutDir:      store.RunDir(),
	}, nil)
	stopProgress()
	log.Printf("proxy shutdown started")
	shutdownErr := stopMeasurementProxy(server)
	if runErr == nil && shutdownErr != nil {
		runErr = shutdownErr
	}
	if runErr != nil {
		return codexSampleResult{}, finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, runErr)
	}
	if !result.Success {
		return codexSampleResult{}, finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, fmt.Errorf("codex exited with code %d: %s", result.ReturnCode, result.Error))
	}
	log.Printf("codex exec completed: return_code=%d wall_ms=%.1f", result.ReturnCode, result.WallMillis)

	reconcileCtx, cancelReconcile := context.WithTimeout(context.Background(), 35*time.Second)
	records, readErr := maybeReconcileUsage(reconcileCtx, store, manifest)

	cancelReconcile()

	if readErr != nil {
		log.Printf("usage reconciliation skipped: %v", readErr)
		records, readErr = metrics.ReadRequestJSONL(store.RequestsPath())
		if readErr != nil {
			return codexSampleResult{}, finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, readErr)
		}
	}
	runMetrics := metrics.FromRequests(runID, records)
	runMetrics.Context.CodexWallMillis = result.WallMillis
	if err := store.WriteMetrics(runMetrics); err != nil {
		return codexSampleResult{}, finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, err)
	}
	finalManifest, err := finalizedManifest(store, manifest, artifacts.RunStatusDone, nil)
	if err != nil {
		return codexSampleResult{}, err
	}
	if err := store.UpdateIndex(finalManifest, runMetrics); err != nil {
		return codexSampleResult{}, err
	}
	log.Printf("artifacts finalized: %s", store.RunDir())
	return codexSampleResult{
		runID:     runID,
		runDir:    store.RunDir(),
		requests:  len(records),
		wallMs:    result.WallMillis,
		costUSD:   runMetrics.Comparable.TotalCostUSD,
		costKnown: runMetrics.Comparable.TotalCostKnown,
	}, nil
}
