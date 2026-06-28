package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/spf13/cobra"

	"router-eval/internal/artifacts"
	"router-eval/internal/metrics"
	"router-eval/internal/routers"
	"router-eval/internal/workloads/probes"
	responsesprobes "router-eval/internal/workloads/probes/responses"
)

type probeOptions struct {
	addr       string
	routerName string
	upstream   string
	runID      string
	outDir     string
	timeout    time.Duration
	name       string
	model      string
	apiKey     string
	samples    int
}

func newProbeCommand() *cobra.Command {
	opts := probeOptions{}
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Run a built-in synthetic probe through the local proxy",
		Example: stringsJoin([]string{
			"router-eval probe --router openrouter --upstream https://openrouter.ai/api --name streaming_responses --model MODEL --samples 30",
			"router-eval probe --router tokenrouter --upstream https://api.tokenrouter.com --name simple_request --model MODEL --samples 30",
		}, "\n"),
		RunE: func(_ *cobra.Command, _ []string) error {
			return runProbe(opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.addr, "addr", "127.0.0.1:0", "local proxy listen address")
	flags.StringVar(&opts.routerName, "router", "", "router adapter name: tokenrouter or openrouter")
	flags.StringVar(&opts.upstream, "upstream", "", "upstream base URL, for example https://api.tokenrouter.com")
	flags.StringVar(&opts.runID, "run-id", "", "run id for artifact output")
	flags.StringVar(&opts.outDir, "out", "out", "artifact root directory")
	flags.DurationVar(&opts.timeout, "timeout", 180*time.Second, "upstream request timeout")
	flags.StringVar(&opts.name, "name", "simple_request", "Responses probe name: simple_request or streaming_responses")
	flags.StringVar(&opts.model, "model", "", "model id to pass through the router")
	flags.StringVar(&opts.apiKey, "proxy-key", "local-proxy-key", "throwaway downstream proxy API key")
	flags.IntVar(&opts.samples, "samples", 30, "number of sequential probe requests to send; percentiles are computed over the samples (30 is the floor for a stable p50; use 100+ for tail/p95)")

	return cmd
}

func runProbe(opts probeOptions) error {
	if opts.routerName == "" {
		return errors.New("--router is required")
	}
	if opts.upstream == "" {
		return errors.New("--upstream is required")
	}
	if opts.runID == "" {
		opts.runID = time.Now().UTC().Format("20060102T150405Z")
	}
	log.Printf("probe setup: run_id=%s router=%s name=%s", opts.runID, opts.routerName, opts.name)

	// Probes target the Responses API only; /v1/chat/completions is legacy.
	request, err := responsesprobes.Request(opts.name, opts.model)
	if err != nil {
		return err
	}

	adapter, err := routers.NewAdapter(opts.routerName)
	if err != nil {
		return err
	}
	store, err := artifacts.NewStore(opts.outDir, opts.runID)
	if err != nil {
		return err
	}
	startedAt := time.Now().UTC()
	manifest := artifacts.Manifest{
		RunID:     opts.runID,
		Router:    adapter.Name(),
		Model:     opts.model,
		Workload:  artifacts.WorkloadRef{Kind: "probe/responses", Name: opts.name},
		Status:    artifacts.RunStatusRunning,
		StartedAt: startedAt,
		Config: map[string]string{
			"upstream": opts.upstream,
			"probe":    request.Name,
			"endpoint": request.Endpoint,
		},
	}
	if err := store.WriteManifest(manifest); err != nil {
		return err
	}
	log.Printf("manifest written: %s", store.ManifestPath())

	server, listener, err := startMeasurementProxy(opts.addr, opts.upstream, opts.outDir, opts.runID, adapter, opts.timeout, opts.apiKey)
	if err != nil {
		return finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, err)
	}
	proxyURL := "http://" + listener.Addr().String()
	log.Printf("proxy started: %s -> %s", proxyURL, opts.upstream)

	samples := opts.samples
	if samples < 1 {
		samples = 1
	}
	runner := probes.Runner{
		ProxyBaseURL: proxyURL,
		APIKey:       opts.apiKey,
	}
	// Sequential samples: each request is measured in isolation (concurrent requests
	// would contend and measure latency under load, not the router's per-request
	// latency). Every request appends its own record to requests.jsonl, so the
	// percentiles in metrics are computed across all samples.
	log.Printf("probe sampling started: %d sequential samples of %s", samples, request.Name)
	var succeeded int
	for i := 0; i < samples; i++ {
		sample, runErr := runner.Run(context.Background(), request)
		if runErr != nil {
			err = runErr
			break
		}
		if sample.StatusCode >= 200 && sample.StatusCode < 400 {
			succeeded++
		}
		log.Printf("sample %d/%d: status=%d bytes=%d duration_ms=%.1f",
			i+1, samples, sample.StatusCode, sample.BodyBytes, float64(sample.Duration.Microseconds())/1000.0)
	}
	log.Printf("proxy shutdown started")
	shutdownErr := stopMeasurementProxy(server)
	if shutdownErr != nil && err == nil {
		err = shutdownErr
	}
	if err != nil {
		return finalizeProbeManifest(store, manifest, artifacts.RunStatusFailed, err)
	}
	log.Printf("probe sampling completed: %d/%d succeeded", succeeded, samples)

	log.Printf("metrics generation started")
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
	fmt.Printf("probe=%s samples=%d succeeded=%d ttfb_p50_ms=%.1f ttft_p50_ms=%.1f e2e_p50_ms=%.1f e2e_p95_ms=%.1f tokens_per_sec=%.1f\n",
		request.Name,
		samples,
		succeeded,
		runMetrics.Comparable.TTFBP50Millis,
		runMetrics.Comparable.TTFTP50Millis,
		runMetrics.Comparable.E2EP50Millis,
		runMetrics.Comparable.E2EP95Millis,
		runMetrics.Comparable.OutputTokensPerSec,
	)
	fmt.Printf("run_id=%s artifacts=%s\n", opts.runID, store.RunDir())
	return nil
}
