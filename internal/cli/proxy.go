package cli

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"router-eval/internal/proxy"
	"router-eval/internal/routers"
)

type proxyOptions struct {
	addr       string
	routerName string
	upstream   string
	runID      string
	outDir     string
	timeout    time.Duration
	apiKey     string
}

func newProxyCommand() *cobra.Command {
	opts := proxyOptions{}
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run only the measurement proxy",
		Example: stringsJoin([]string{
			"router-eval proxy --router tokenrouter --upstream https://api.tokenrouter.com --run-id RUN",
			"router-eval proxy --router openrouter --upstream https://openrouter.ai/api --addr 127.0.0.1:8080",
		}, "\n"),
		RunE: func(_ *cobra.Command, _ []string) error {
			return runProxy(opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.addr, "addr", "127.0.0.1:8080", "local proxy listen address")
	flags.StringVar(&opts.routerName, "router", "", "router adapter name: tokenrouter or openrouter")
	flags.StringVar(&opts.upstream, "upstream", "", "upstream base URL, for example https://openrouter.ai/api")
	flags.StringVar(&opts.runID, "run-id", "", "run id for artifact output")
	flags.StringVar(&opts.outDir, "out", "out", "artifact root directory")
	flags.DurationVar(&opts.timeout, "timeout", 180*time.Second, "upstream request timeout")
	flags.StringVar(&opts.apiKey, "proxy-key", "local-proxy-key", "throwaway downstream proxy API key")

	return cmd
}

func runProxy(opts proxyOptions) error {
	if opts.routerName == "" {
		return errors.New("--router is required")
	}
	if opts.upstream == "" {
		return errors.New("--upstream is required")
	}
	if opts.runID == "" {
		opts.runID = time.Now().UTC().Format("20060102T150405Z")
	}

	adapter, err := routers.NewAdapter(opts.routerName)
	if err != nil {
		return err
	}

	server, err := proxy.NewServer(proxy.Config{
		Addr:        opts.addr,
		Upstream:    opts.upstream,
		OutDir:      opts.outDir,
		RunID:       opts.runID,
		Router:      adapter,
		HTTPClient:  &http.Client{Timeout: opts.timeout},
		ProxyAPIKey: opts.apiKey,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("router-eval proxy listening on http://%s for run %s", opts.addr, opts.runID)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
