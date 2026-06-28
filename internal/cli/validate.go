package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type validateOptions struct {
	workload string
}

func newValidateCommand(execLookPath func(file string) (string, error)) *cobra.Command {
	opts := validateOptions{}
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate local prerequisites for probe and harness workloads",
		Example: stringsJoin([]string{
			"router-eval validate --workload all",
			"router-eval validate --workload probes",
			"router-eval validate --workload harness",
		}, "\n"),
		RunE: func(_ *cobra.Command, _ []string) error {
			return runValidate(opts, execLookPath)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.workload, "workload", "all", "workload to validate: probes, harness, or all")
	return cmd
}

func runValidate(opts validateOptions, execLookPath func(file string) (string, error)) error {
	switch opts.workload {
	case "all", "probes", "harness":
	default:
		return fmt.Errorf("unknown workload %q", opts.workload)
	}

	var failed bool
	if opts.workload == "all" || opts.workload == "probes" {
		if !printProbeValidation() {
			failed = true
		}
	}
	if opts.workload == "all" || opts.workload == "harness" {
		if !printHarnessValidation(execLookPath) {
			failed = true
		}
	}
	if failed {
		return errors.New("validation failed")
	}
	return nil
}

func printProbeValidation() bool {
	fmt.Println("Probe evaluation")
	checks := []validationCheck{
		{"OPENROUTER_API_KEY", os.Getenv("OPENROUTER_API_KEY") != "", "required for OpenRouter probes"},
		{"OPENROUTER_MGMT_KEY", os.Getenv("OPENROUTER_API_KEY") == "" || os.Getenv("OPENROUTER_MGMT_KEY") != "", "required for OpenRouter usage reconciliation"},
		{"TOKENROUTER_API_KEY", os.Getenv("TOKENROUTER_API_KEY") != "", "required for tokenRouter probes"},
		{"MGMT_KEY", os.Getenv("TOKENROUTER_API_KEY") == "" || tokenRouterManagementKeyConfigured(), "required for tokenRouter usage reconciliation"},
	}
	return printChecks(checks, true)
}

func printHarnessValidation(execLookPath func(file string) (string, error)) bool {
	fmt.Println("Codex harness evaluation")
	_, codexErr := execLookPath("codex")
	checks := []validationCheck{
		{"codex CLI", codexErr == nil, "required for Codex harness runs"},
		{"OPENROUTER_API_KEY", os.Getenv("OPENROUTER_API_KEY") != "", "required for OpenRouter harness runs"},
		// OPENROUTER_MGMT_KEY is intentionally not required for the harness: Codex
		// /v1/responses traffic's gen-id is not actually the billable openrouter gen-id which I haven't figured out why yet
		// OpenRouter cost is reconciled from a manually downloaded activity CSV
		// (`reconcile --csv`), not the live generation-lookup API.
		{"TOKENROUTER_API_KEY", os.Getenv("TOKENROUTER_API_KEY") != "", "required for tokenRouter harness runs"},
		{"MGMT_KEY", os.Getenv("TOKENROUTER_API_KEY") == "" || tokenRouterManagementKeyConfigured(), "required for tokenRouter usage reconciliation"},
	}
	return printChecks(checks, true)
}

func tokenRouterManagementKeyConfigured() bool {
	return os.Getenv("MGMT_KEY") != ""
}

type validationCheck struct {
	Name string
	OK   bool
	Help string
}

func printChecks(checks []validationCheck, allowPartialRouterKeys bool) bool {
	var routerKeyOK bool
	allRequiredOK := true
	for _, check := range checks {
		status := "ok"
		if !check.OK {
			status = "missing"
			if strings.Contains(check.Name, "API_KEY") {
				// At least one router key is enough to run that workload type.
			} else {
				allRequiredOK = false
			}
		}
		if check.OK && strings.Contains(check.Name, "API_KEY") {
			routerKeyOK = true
		}
		fmt.Printf("  [%s] %s", status, check.Name)
		if !check.OK {
			fmt.Printf(" - %s", check.Help)
		}
		fmt.Println()
	}
	if allowPartialRouterKeys && !routerKeyOK {
		allRequiredOK = false
	}
	fmt.Println()
	return allRequiredOK
}
