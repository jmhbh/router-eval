package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"router-eval/internal/artifacts"
)

func TestRootHelpShowsImplementedCommandsOnly(t *testing.T) {
	cmd := NewRootCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	help := out.String()
	for _, want := range []string{"probe", "codex", "validate", "reconcile", "ui-serve", "ui-stop", "proxy"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
	for _, unwanted := range []string{"report"} {
		if strings.Contains(help, unwanted) {
			t.Fatalf("help unexpectedly contains %q:\n%s", unwanted, help)
		}
	}
}

func TestImplementedSubcommandHelpWorks(t *testing.T) {
	subcommands := []string{"probe", "codex", "validate", "reconcile", "ui-serve", "ui-stop", "proxy"}
	for _, subcommand := range subcommands {
		t.Run(subcommand, func(t *testing.T) {
			cmd := NewRootCommand(Options{})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{subcommand, "--help"})

			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}

			help := out.String()
			if !strings.Contains(help, "Usage:") {
				t.Fatalf("expected usage for %q, got:\n%s", subcommand, help)
			}
		})
	}
}

func TestTokenRouterWindowUsesTightWiggle(t *testing.T) {
	endedAt := time.Unix(200, 0).UTC()
	window := tokenRouterWindow(artifacts.Manifest{
		StartedAt: time.Unix(100, 0).UTC(),
		EndedAt:   &endedAt,
	}, 10*time.Second)
	if window.StartUnix != 90 || window.EndUnix != 210 {
		t.Fatalf("window=%+v", window)
	}
}
