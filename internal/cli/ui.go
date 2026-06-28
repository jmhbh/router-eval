package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"router-eval/internal/ui"
)

type uiServeOptions struct {
	addr        string
	outDir      string
	background  bool
	pidFilePath string
}

func newUIServeCommand() *cobra.Command {
	opts := uiServeOptions{}
	cmd := &cobra.Command{
		Use:   "ui-serve",
		Short: "Serve the local artifacts dashboard",
		Example: stringsJoin([]string{
			"router-eval ui-serve --out out --addr 127.0.0.1:8080",
			"router-eval ui-serve --out out --addr 127.0.0.1:8080 --background",
		}, "\n"),
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUIServe(opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.addr, "addr", "127.0.0.1:8080", "local UI listen address")
	flags.StringVar(&opts.outDir, "out", "out", "artifact root directory")
	flags.BoolVar(&opts.background, "background", false, "start UI server in the background")
	flags.StringVar(&opts.pidFilePath, "pid-file-path", defaultUIPIDFilePath(), "path to write the background UI server PID")

	return cmd
}

func runUIServe(opts uiServeOptions) error {
	if !strings.HasPrefix(opts.addr, "127.0.0.1:") && !strings.HasPrefix(opts.addr, "localhost:") {
		return errors.New("ui-serve only supports localhost addresses")
	}
	if opts.background {
		return startUIBackground(opts.addr, opts.outDir, opts.pidFilePath)
	}
	server, err := ui.NewServer(opts.outDir)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              opts.addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		log.Printf("router-eval UI listening on http://%s", opts.addr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type uiStopOptions struct {
	pidFilePath string
}

func newUIStopCommand() *cobra.Command {
	opts := uiStopOptions{}
	cmd := &cobra.Command{
		Use:   "ui-stop",
		Short: "Stop a background UI server",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUIStop(opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.pidFilePath, "pid-file-path", defaultUIPIDFilePath(), "path to the UI server PID file")
	return cmd
}

func runUIStop(opts uiStopOptions) error {
	pid, err := readPIDFile(opts.pidFilePath)
	if err != nil {
		return err
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !processAlreadyFinished(err) {
		return err
	}
	if err := os.Remove(opts.pidFilePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("stopped ui server pid=%d\n", pid)
	return nil
}

func startUIBackground(addr, outDir, pidFilePath string) error {
	if _, err := os.Stat(pidFilePath); err == nil {
		return fmt.Errorf("pid file already exists: %s", pidFilePath)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "ui-serve", "--addr", addr, "--out", outDir, "-pid-file-path", pidFilePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := writePIDFile(pidFilePath, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		return err
	}
	fmt.Printf("ui server started in background pid=%d url=http://%s pid_file=%s\n", cmd.Process.Pid, addr, pidFilePath)
	return nil
}
