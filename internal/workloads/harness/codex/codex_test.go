package codex

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPrepareHomeWritesIsolatedConfig(t *testing.T) {
	home, err := PrepareHome(t.TempDir(), Config{
		Model:    "openai/gpt-oss-120b:free",
		ProxyURL: "http://127.0.0.1:8080/v1/",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(home.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`model = "openai/gpt-oss-120b:free"`,
		`base_url = "http://127.0.0.1:8080/v1"`,
		`wire_api = "responses"`,
		`requires_openai_auth = false`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestRunBuildsCodexCommandAndWritesLogs(t *testing.T) {
	runner := &recordingRunner{
		result: CommandResult{
			Stdout: []byte(`{"type":"message"}` + "\n"),
			Stderr: []byte("stderr\n"),
		},
	}
	outDir := t.TempDir()
	result, err := Run(context.Background(), Config{
		Model:       "openai/gpt-oss-120b:free",
		Prompt:      "hello",
		Workdir:     t.TempDir(),
		ProxyURL:    "http://127.0.0.1:1234/v1",
		ProxyAPIKey: "proxy-key",
		Timeout:     time.Minute,
		OutDir:      outDir,
		Approval:    "never",
		Sandbox:     "workspace-write",
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("result=%+v", result)
	}
	if runner.command.Name != "codex" || !containsArg(runner.command.Args, "exec") {
		t.Fatalf("command=%+v", runner.command)
	}
	if runner.command.Stdin != "hello" {
		t.Fatalf("stdin=%q", runner.command.Stdin)
	}
	if !containsArgPair(runner.command.Args, "--ask-for-approval", "never") {
		t.Fatalf("missing approval args: %+v", runner.command.Args)
	}
	if !containsArgPair(runner.command.Args, "--sandbox", "workspace-write") {
		t.Fatalf("missing sandbox args: %+v", runner.command.Args)
	}
	if !containsEnv(runner.command.Env, "PROXY_KEY=proxy-key") {
		t.Fatalf("missing PROXY_KEY env")
	}
	if !containsEnvPrefix(runner.command.Env, "CODEX_HOME=") {
		t.Fatalf("missing CODEX_HOME env")
	}
	stdout, err := os.ReadFile(result.StdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdout), `"type":"message"`) {
		t.Fatalf("stdout=%s", stdout)
	}
}

type recordingRunner struct {
	command Command
	result  CommandResult
}

func (r *recordingRunner) Run(ctx context.Context, command Command) CommandResult {
	r.command = command
	return r.result
}

func containsEnv(env []string, want string) bool {
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

func containsEnvPrefix(env []string, prefix string) bool {
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, key string, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}
