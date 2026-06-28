package cli

import (
	"os/exec"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
)

const routerEvalBanner = ` ____   ___  _   _ _____ _____ ____        _______     ___    _
|  _ \ / _ \| | | |_   _| ____|  _ \      | ____\ \   / / \  | |
| |_) | | | | | | | | | |  _| | |_) |_____|  _|  \ \ / / _ \ | |
|  _ <| |_| | |_| | | | | |___|  _ <______| |___  \ V / ___ \| |___
|_| \_\\___/ \___/  |_| |_____|_| \_\     |_____|  \_/_/   \_\_____|`

// Options configures CLI dependencies.
type Options struct {
	ExecLookPath func(file string) (string, error)
}

func withDefaults(opts Options) Options {
	if opts.ExecLookPath == nil {
		opts.ExecLookPath = exec.LookPath
	}
	return opts
}

// NewRootCommand builds the router-eval root command.
func NewRootCommand(opts Options) *cobra.Command {
	opts = withDefaults(opts)

	cmd := &cobra.Command{
		Use:           "router-eval",
		Short:         "Evaluate hosted LLM routers via a local measurement proxy",
		Long:          routerEvalBanner + "\n\nEvaluate hosted LLM routers (tokenRouter, OpenRouter) on cost, performance,\nand reliability via a transparent local measurement proxy.",
		Version:       resolveVersion(),
		SilenceErrors: true,
		SilenceUsage:  true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		Example: stringsJoin([]string{
			"router-eval probe --router tokenrouter --upstream https://api.tokenrouter.com --name simple_request --model MODEL",
			"router-eval codex --router openrouter --upstream https://openrouter.ai/api --model MODEL --task issue-123 --prompt \"...\"",
			"router-eval validate --workload all",
			"router-eval reconcile --run-id RUN --out out",
			"router-eval ui-serve --out out --addr 127.0.0.1:8080 --background",
			"router-eval ui-stop",
			"router-eval proxy --router tokenrouter --upstream https://api.tokenrouter.com --run-id RUN",
		}, "\n"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.SetVersionTemplate("router-eval version {{.Version}}\n")

	cmd.AddCommand(
		newProbeCommand(),
		newCodexCommand(),
		newValidateCommand(opts.ExecLookPath),
		newReconcileCommand(),
		newUIServeCommand(),
		newUIStopCommand(),
		newProxyCommand(),
	)

	return cmd
}

// Execute runs the CLI with an argv slice.
func Execute(args []string, opts Options) error {
	cmd := NewRootCommand(opts)
	if len(args) > 0 {
		cmd.Use = args[0]
		cmd.SetArgs(normalizeLegacyLongFlags(args[1:]))
	}
	return cmd.Execute()
}

// normalizeLegacyLongFlags preserves existing `-flag` usage from the previous
// stdlib flag-based CLI while using Cobra/pflag.
func normalizeLegacyLongFlags(args []string) []string {
	normalized := make([]string, 0, len(args))
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || arg == "-" {
			normalized = append(normalized, arg)
			continue
		}
		name := arg[1:]
		if eq := strings.Index(name, "="); eq >= 0 {
			name = name[:eq]
		}
		if len(name) == 1 || name == "" {
			normalized = append(normalized, arg)
			continue
		}
		r := rune(name[0])
		if unicode.IsDigit(r) {
			normalized = append(normalized, arg)
			continue
		}
		normalized = append(normalized, "--"+arg[1:])
	}
	return normalized
}

func stringsJoin(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, sep)
}
