# router-eval

CLI plus transparent local measurement proxy for evaluating hosted LLM
aggregators on cost, performance, and reliability while holding the model fixed.

## Prerequisites

- Go 1.22 or newer.
- Network access to the hosted router APIs being evaluated.
- A tokenRouter account and API key. For Codex setup background, see:
  https://www.tokenrouter.com/docs/set-up-tokenrouter-in-codex
- An OpenRouter account and API key. For Codex CLI setup background, see:
  https://openrouter.ai/docs/cookbook/coding-agents/codex-cli
- Codex CLI installed when running the Codex harness.
- Install the latest CLI release (auto-detects OS/arch): `curl -fsSL https://raw.githubusercontent.com/jmhbh/router-eval/main/install.sh | sh`

Run the `validate` cmd to ensure necessary environment variables are set.


## Usage

The CLI supports gathering metrics around simple request probes that are served via the UI
or alternatively gathering request metrics, including tool calls through a harness. Currently,
only codex is supported.

## Running probes

Run a simple OpenRouter Responses probe through the local measurement proxy:

```sh
router-eval probe \
  --router openrouter \
  --upstream https://openrouter.ai/api \
  --name simple_request \
  --model openai/gpt-oss-120b:free
```

Run a long-context tokenRouter Responses probe:

```sh
router-eval probe \
  --router tokenrouter \
  --upstream https://api.tokenrouter.com \
  --name long_context \
  --model qwen/qwen3.5-9b
```

## Running a Codex task

Run a Codex task through the local measurement proxy:

```sh
bin/router-eval codex \
  --router openrouter \
  --upstream https://openrouter.ai/api \
  --model openai/gpt-oss-120b:free \
  --task create-snake-game \
  --sandbox workspace-write \
  --approval never \
  --prompt "$(cat snake_prompt.txt)"
```

The harness creates an isolated `CODEX_HOME`, points Codex at the local proxy via
generated config, and stores Codex stdout/stderr under the run artifact
directory.

## UI

Serve the local dashboard in the foreground:

```sh
bin/router-eval ui-serve --out out --addr 127.0.0.1:8080
```

Serve it in the background:

```sh
bin/router-eval ui-serve --out out --addr 127.0.0.1:8080 --background
```

Stop a background UI server:

```sh
bin/router-eval ui-stop
```

Both `ui-serve --background` and `ui-stop` support `--pid-file-path`. By default
the PID file is written under the system temp directory.


## Manual Cost Reconciliation for Codex tasks (OpenRouter only)

Costs metrics are automatically gathered and reconciled when using token router when running the
`codex` cmd. When using openrouter cost reconiliation needs to be performed manually via
the following instructions due to OpenRouter's response `gen-id` not being a billable generation-id.
`RUN_ID` can be found in the `out` directory.

```sh
router-eval reconcile --run-id <RUN_ID> --out out --csv openrouter_activity.csv
```

Export the OpenRouter activity CSV for a window that covers the run **in UTC**
(the dashboard typically filters by local time, while the generation second is
UTC). A reconciliation summary is written to
`out/runs/<RUN>/reconciliation/summary.json`.
