package codex

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Model       string
	Prompt      string
	Workdir     string
	ProxyURL    string
	ProxyAPIKey string
	Timeout     time.Duration
	Sandbox     string
	Approval    string
	OutDir      string
}

type Result struct {
	Success          bool
	WallMillis       float64
	RouterBusyMillis float64
	StdoutPath       string
	StderrPath       string
	CodexHome        string
	ReturnCode       int
	TimedOut         bool
	Error            string
}

type Home struct {
	Path       string
	ConfigPath string
}

// PrepareHome creates an isolated CODEX_HOME for one harness run.
// Codex is pointed at the local measurement proxy via config.toml; no /etc/hosts
// mapping, global Codex config mutation, or transparent network interception is
// used. The subprocess should set CODEX_HOME=Home.Path and PROXY_KEY to any
// throwaway value because the proxy injects the real upstream router credential.
func PrepareHome(parentDir string, config Config) (Home, error) {
	if config.Model == "" {
		return Home{}, fmt.Errorf("model is required")
	}
	if config.ProxyURL == "" {
		return Home{}, fmt.Errorf("proxy URL is required")
	}
	if parentDir == "" {
		parentDir = os.TempDir()
	}
	home, err := os.MkdirTemp(parentDir, "router-eval-codex-*")
	if err != nil {
		return Home{}, err
	}
	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte(renderConfig(config)), 0o600); err != nil {
		return Home{}, err
	}
	return Home{Path: home, ConfigPath: configPath}, nil
}

// renderConfig renders the Codex config to use our local proxy
func renderConfig(config Config) string {
	return fmt.Sprintf(`model = %q
model_provider = "local_proxy"

[model_providers.local_proxy]
name = "Local Measurement Proxy"
base_url = %q
env_key = "PROXY_KEY"
wire_api = "responses"
requires_openai_auth = false
`, config.Model, strings.TrimRight(config.ProxyURL, "/"))
}

type CommandRunner interface {
	Run(ctx context.Context, command Command) CommandResult
}

type Command struct {
	Name  string
	Args  []string
	Dir   string
	Env   []string
	Stdin string
}

type CommandResult struct {
	Stdout     []byte
	Stderr     []byte
	ReturnCode int
	Err        error
	TimedOut   bool
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command Command) CommandResult {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = command.Env
	if command.Stdin != "" {
		cmd.Stdin = strings.NewReader(command.Stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := CommandResult{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
		Err:    err,
	}
	if cmd.ProcessState != nil {
		result.ReturnCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ReturnCode = 124
	}
	return result
}

func Run(ctx context.Context, config Config, runner CommandRunner) (Result, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	if config.Prompt == "" {
		return Result{}, fmt.Errorf("prompt is required")
	}
	if config.Workdir == "" {
		config.Workdir = "."
	}
	if config.ProxyAPIKey == "" {
		config.ProxyAPIKey = "local-proxy-key"
	}
	if config.Sandbox == "" {
		config.Sandbox = "read-only"
	}
	if config.Approval == "" {
		config.Approval = "never"
	}
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Minute
	}
	if config.OutDir == "" {
		config.OutDir = "."
	}
	if err := os.MkdirAll(config.OutDir, 0o755); err != nil {
		return Result{}, err
	}

	home, err := PrepareHome("", config)
	if err != nil {
		return Result{}, err
	}
	// CODEX_HOME is per-run and isolated; codex fills it with sqlite state, sessions,
	// and a plugins clone (tens of MB). Remove it once the subprocess has exited —
	// stdout/stderr are persisted to OutDir below, so nothing measured is lost.
	defer os.RemoveAll(home.Path)

	runCtx, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()

	env := append(os.Environ(),
		"CODEX_HOME="+home.Path,
		"PROXY_KEY="+config.ProxyAPIKey,
	)
	command := Command{
		Name: "codex",
		Args: []string{
			"--ask-for-approval", config.Approval,
			"exec",
			"--skip-git-repo-check",
			"--sandbox", config.Sandbox,
			"--color", "never",
			"--json",
			"-",
		},
		Dir:   config.Workdir,
		Env:   env,
		Stdin: config.Prompt,
	}

	start := time.Now()
	commandResult := runner.Run(runCtx, command)
	wall := time.Since(start)

	stdoutPath := filepath.Join(config.OutDir, "codex.stdout.jsonl")
	stderrPath := filepath.Join(config.OutDir, "codex.stderr")
	if err := writeFile(stdoutPath, commandResult.Stdout); err != nil {
		return Result{}, err
	}
	stderr := commandResult.Stderr
	if commandResult.TimedOut {
		stderr = append(stderr, []byte(fmt.Sprintf("\nTIMEOUT: codex exceeded %s\n", config.Timeout))...)
	}
	if err := writeFile(stderrPath, stderr); err != nil {
		return Result{}, err
	}

	result := Result{
		Success:    commandResult.Err == nil && commandResult.ReturnCode == 0,
		WallMillis: float64(wall.Microseconds()) / 1000.0,
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
		CodexHome:  home.Path,
		ReturnCode: commandResult.ReturnCode,
		TimedOut:   commandResult.TimedOut,
	}
	if commandResult.Err != nil {
		result.Error = commandResult.Err.Error()
	}
	return result, nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if data == nil {
		data = []byte{}
	}
	return os.WriteFile(path, data, 0o644)
}
