package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

	return cmd
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
	if opts.runID == "" {
		opts.runID = time.Now().UTC().Format("20060102T150405Z")
	}
	log.Printf("codex setup: run_id=%s router=%s task=%s", opts.runID, opts.routerName, opts.task)

	adapter, err := routers.NewAdapter(opts.routerName)
	if err != nil {
		return err
	}
	store, err := artifacts.NewStore(opts.outDir, opts.runID)
	if err != nil {
		return err
	}
	manifest := artifacts.Manifest{
		RunID:     opts.runID,
		Router:    adapter.Name(),
		Model:     opts.model,
		Workload:  artifacts.WorkloadRef{Kind: "harness/codex", Name: opts.task},
		Status:    artifacts.RunStatusRunning,
		StartedAt: time.Now().UTC(),
		Config: map[string]string{
			"upstream": opts.upstream,
			"workdir":  opts.workdir,
			"sandbox":  opts.sandbox,
			"approval": opts.approval,
		},
	}
	if err := store.WriteManifest(manifest); err != nil {
		return err
	}
	promptPath := filepath.Join(store.RunDir(), "prompt.txt")
	if err := os.MkdirAll(store.RunDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(promptPath, []byte(opts.prompt), 0o600); err != nil {
		return err
	}
	manifest.Config["prompt_path"] = promptPath
	if err := store.WriteManifest(manifest); err != nil {
		return err
	}

	server, listener, err := startMeasurementProxy(opts.addr, opts.upstream, opts.outDir, opts.runID, adapter, opts.timeout, opts.apiKey)
	if err != nil {
		return finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, err)
	}
	proxyURL := "http://" + listener.Addr().String() + "/v1"
	log.Printf("proxy started: %s -> %s", proxyURL, opts.upstream)

	log.Printf("codex exec started")
	stopProgress := startRequestProgressLogger(context.Background(), store, 2*time.Second)
	result, runErr := codexharness.Run(context.Background(), codexharness.Config{
		Model:       opts.model,
		Prompt:      opts.prompt,
		Workdir:     opts.workdir,
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
		return finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, runErr)
	}
	if !result.Success {
		return finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, fmt.Errorf("codex exited with code %d: %s", result.ReturnCode, result.Error))
	}
	log.Printf("codex exec completed: return_code=%d wall_ms=%.1f", result.ReturnCode, result.WallMillis)

	reconcileCtx, cancelReconcile := context.WithTimeout(context.Background(), 35*time.Second)
	records, readErr := maybeReconcileUsage(reconcileCtx, store, manifest)

	cancelReconcile()

	if readErr != nil {
		log.Printf("usage reconciliation skipped: %v", readErr)
		records, readErr = metrics.ReadRequestJSONL(store.RequestsPath())
		if readErr != nil {
			return finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, readErr)
		}
	}
	runMetrics := metrics.FromRequests(opts.runID, records)
	runMetrics.Context.CodexWallMillis = result.WallMillis
	if err := store.WriteMetrics(runMetrics); err != nil {
		return finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, err)
	}
	finalManifest, err := finalizedManifest(store, manifest, artifacts.RunStatusDone, nil)
	if err != nil {
		return err
	}
	if err := store.UpdateIndex(finalManifest, runMetrics); err != nil {
		return err
	}
	log.Printf("artifacts finalized: %s", store.RunDir())
	fmt.Printf("codex task=%s requests=%d wall_ms=%.1f\n", opts.task, len(records), result.WallMillis)
	fmt.Printf("run_id=%s artifacts=%s\n", opts.runID, store.RunDir())
	return nil
}
