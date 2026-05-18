# Scion Localhost Workflows

## Prerequisites

Container runtime (auto-detected): Podman, Docker, or `container` (macOS). Also `git`, `tmux`.

```bash
scion doctor
```

## Project Setup

```bash
# Machine-level setup (one-time)
scion init --machine --image-registry localhost:5000

# Project init (inside a git repo)
scion init
```

## Running Agents

```bash
scion start <name> [task...]              # Launch agent
scion start <name> -a [task...]           # Launch + attach to TTY
scion start <name> --harness opencode     # Use opencode harness
scion start <name> --harness-auth none    # Required for opencode (not --no-auth)

scion list                                # List agents
scion look <name>                         # View terminal output
scion logs <name>                         # View logs (NOT --follow, errors in hub mode)
scion attach <name>                       # Attach to tmux session
scion message <name> "text"               # Send input to agent

scion delete <name>                       # Remove agent (use delete, not stop — stop leaves stale state)
```

Use `--no-hub` on any command for local-only mode.

## Troubleshooting

### Agent stalled

```bash
scion logs <name>
scion look <name>
```

Hub connectivity failure or LLM API error.

### Heartbeat failures

```
[ERROR] Heartbeat failed: connection refused
```

Start the Hub server or use `--no-hub`.

### Image not found

```bash
./image-build/scripts/build-images.sh --registry <registry> --push
```

## Developing Scion on localhost

### Local Registry

```bash
docker run -d registry:3 -p 5000:5000 registry
```

### Custom Buildx Builder

Buildx is configured via `buildkitd.toml` in the repo.

```bash
docker buildx create --name scion-builder --driver-opt network=host --config buildkitd.toml --use
```

### Building

First time:
```bash
make web
```

Install scion CLI:
```bash
go install ./cmd/scion
```

Build container images:
```bash
./image-build/scripts/build-images.sh --registry localhost:5000 --target all --push
```

Target-specific: `core-base`, `scion-base`, `opencode`, `claude`, `codex`, `hub`.

### Llama.cpp (Model Serving)

llama.cpp serves GGUF models via OpenAI-compatible API, faking the Anthropic API for agents. Set `ANTHROPIC_BASE_URL` in settings to point at the llama.cpp server:

```yaml
profiles:
  local:
    env:
      ANTHROPIC_AUTH_TOKEN: local
      ANTHROPIC_BASE_URL: http://host.docker.internal:8091   # macOS
      # ANTHROPIC_BASE_URL: http://172.17.0.1:8091           # Linux
```

Ports used: 8080 (general), 8091 (coding, MTP speculative decoding).

### Agent Config (opencode.json)

When using the opencode harness, the agent's `~/.config/opencode/opencode.json` is seeded from `~/.scion/harness-configs/opencode/home/.config/opencode/opencode.json`. This file should mirror your local `~/.config/opencode/opencode.jsonc` but with two key differences:

1. **Remove the `mcp` section** — MCP servers are not available inside the agent container.
2. **Use `host.docker.internal`** instead of your local LAN IP for llama.cpp endpoints, since the agent runs inside a Docker container.

Example `~/.scion/harness-configs/opencode/home/.config/opencode/opencode.json`:

```json
{
"$schema": "https://opencode.ai/config.json",

  "model": "llama.cpp-exp/Qwen3.6-35B-A3B-MTP",

  "provider": {
    "llama.cpp-gen": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "llama-server (gen)",
      "options": {
        "baseURL": "http://host.docker.internal:8090/v1"
      },
      "models": {
        "Qwen3.6-35B-A3B-MTP": {
          "name": "Qwen3.6-35B-A3B-MTP (general)",
          "limit": {
            "context": 262144,
            "output": 32768
          }
        }
      }
    },
    "llama.cpp-exp": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "llama-server (exp)",
      "options": {
        "baseURL": "http://host.docker.internal:8091/v1"
      },
      "models": {
        "Qwen3.6-35B-A3B-MTP": {
          "name": "Qwen3.6-35B-A3B-MTP (coding)",
          "limit": {
            "context": 262144,
            "output": 32768
          }
        }
      }
    }
  },

  "permission": {
    "lsp": "allow",
    "external_directory": {
      "~/.scion/**": "allow",
      "~/.config/**": "allow",
      "/tmp/**": "allow"
    }
  },

  "formatter": true,
  "lsp": true
}
```

To rebuild the harness config from your local config: copy `~/.config/opencode/opencode.jsonc` → `~/.scion/harness-configs/opencode/home/.config/opencode/opencode.json`, remove the `mcp` block, and replace any local LAN IPs with `host.docker.internal`.

### Scion Server (Hub + Broker)

```bash
./scripts/local-scion-server.sh
```

The script kills any existing server, starts a new one in the background, and waits for it to be ready before returning.

### Settings

After `scion init --machine`, edit `~/.scion/settings.yaml`:
```yaml
image_registry: localhost:5000
default_harness_config: opencode
hub:
  enabled: true
  endpoint: http://localhost:9810
server:
  broker:
    container_hub_endpoint: http://host.docker.internal:9810   # macOS
    # container_hub_endpoint: http://172.17.0.1:9810           # Linux
profiles:
  local:
    runtime: docker
    env:
      ANTHROPIC_AUTH_TOKEN: local
      ANTHROPIC_BASE_URL: http://host.docker.internal:8091
```

## Lifecycle Testing an Agent

### Process

1. **Start server** — `./scripts/local-scion-server.sh`
   - Blocks until server is ready, then returns. Server keeps running in background.
   - To restart: run the script again — it kills the old server automatically.

2. **Start agent** — `scion start test-agent "task" --harness opencode --harness-auth none`
   - `--harness-auth none` is required for opencode (not `--no-auth`)

3. **Wait 20s, then verify via Hub API** — `curl http://localhost:9810/api/v1/agents`
   - The Hub API is the source of truth, not container logs
   - Check `lastActivityEvent` updates — if it moves, the plugin is firing

4. **Check agent-info.json inside container** — `docker exec <container> cat /home/scion/agent-info.json`
   - If timestamps are `0001-01-01T00:00:00Z`, the hub endpoint is unreachable from the container

5. **Clean up** — `scion delete test-agent`
   - Use `delete`, not `stop`. Stop leaves stale state.

### Gotchas

- **macOS uses `host.docker.internal`, not `172.17.0.1`** for container-to-host networking.
  - `SCION_HUB_ENDPOINT` inside containers defaults to `localhost:9810` — unreachable from Docker.
  - Fix: set `server.broker.container_hub_endpoint: http://host.docker.internal:9810` in `~/.scion/settings.yaml`
  - Server must be restarted after settings changes (not hot-reloaded)

- **`agent-info.json` is stale when hub is unreachable.** Don't use it to diagnose — check Hub API instead.

- **`scion logs --follow` errors in hub mode.** Use `scion logs <name>` (tail) instead.

- **Plugin loading is verified in OpenCode UI**, not by looking for `/tmp/scion-hook-*` files.
  - OpenCode shows "1 Plugin: scion-plugin" in the bottom panel when loaded.

- **`--no-auth` alone is not enough for opencode.** Need `--harness-auth none`.

## Multi-Agent Collaboration Tests

### Purpose

Test that scion can orchestrate multiple agents that communicate via `scion message`. The goal is to verify:

1. An orchestrator agent can start a child agent, send it a task, wait for the result, and produce an output file
2. The child agent can receive a task (via start message or post-start message), perform analysis, and send results back
3. The messaging system works end-to-end: send → deliver → receive → respond → receive
4. The `blocked` status + harness notification correctly resumes the orchestrator when a message arrives

Run these tests sequentially, trying different approaches for how the task reaches the child agent. For each approach, record results below.

### Known Issues to Work Around

- **Messaging race condition**: If an agent starts another agent and immediately sends it a message, the message may be lost because the target agent isn't ready yet. Always wait for the target agent to reach `running` phase before sending messages.
- **Orchestrator shortcutting**: The orchestrator agent tends to skip waiting steps and proceed to output steps before receiving messages. Always set `blocked` status after sending the child agent's task — the harness will auto-notify when a message arrives. Do NOT have the orchestrator poll for messages.

### Cleanup

Always use `scion delete`, not `scion stop` — stop leaves stale state.

---

### Code Review Test — Approach 1: Task in Post-Start Message

Orchestrator starts code-reviewer with no task, waits for it to be ready, then sends the task via `scion message`.

**Orchestrator task** (pass inline via command line):
```
You are the orchestrator. Coordinate with a code-reviewer agent to analyze three function specifications.

1. Start a code-reviewer agent (no task, just boot it): `scion start code-reviewer -y -t default --harness opencode --harness-auth none`
2. Wait until code-reviewer's phase is `running` (poll with `scion list --format json`), then send it this task:

   Analyze these three function specifications and provide a detailed review:

   FUNCTION 1: calculateSum(numbers)
   - Description: Sums all numbers in a list
   - Input: array of integers
   - Output: integer sum
   - Edge cases: empty list returns 0, negative numbers allowed

   FUNCTION 2: findMax(numbers)
   - Description: Finds the maximum value in a list
   - Input: array of integers
   - Output: maximum integer
   - Edge cases: empty list returns null, single element returns itself

   FUNCTION 3: reverseString(s)
   - Description: Reverses a string
   - Input: string
   - Output: reversed string
   - Edge cases: empty string returns empty, single char returns itself

   Provide:
   (1) Time and space complexity for each (O notation)
   (2) Whether each edge case is handled correctly and if any are missing
   (3) One actionable improvement suggestion per function

   After completing your analysis, send your report back to me via:
   scion message orchestrator "YOUR COMPLETE REPORT HERE WITH ALL THREE SECTIONS"

3. Set blocked status: `sciontool status blocked "Waiting for code-reviewer analysis"`
4. Wait for code-reviewer's message (harness will notify you)
5. Once you receive the report, create `/workspace/review-summary.md` containing:
   - A header section
   - code-reviewer's full report (copy their findings)
   - Your own brief assessment section at the end evaluating whether the functions are production-ready
6. Mark complete: `sciontool status task_completed "Multi-agent code review complete"`
```

**Start command**:
```bash
TASK=$(echo '...' | sed 's/"/\\"/g')
scion start orchestrator -y -t default --harness opencode --harness-auth none "$TASK"
```

**Monitor and validate**:
- code-reviewer starts and reaches `running` phase
- Orchestrator waits for code-reviewer to be ready before sending the message
- code-reviewer receives the task, performs analysis, and sends `scion message orchestrator "..."`
- Orchestrator receives the message and proceeds past blocked status
- `/workspace/review-summary.md` is created with the expected content (header, report, assessment)
- Orchestrator reaches `task_completed` phase

---

### Code Review Test — Approach 2: Task in code-reviewer's Start Message

Orchestrator starts code-reviewer with the full task embedded in its initial `scion start` message. This avoids the race condition entirely since the task is delivered during boot.

**Orchestrator task** (pass inline via command line):
```
You are the orchestrator. Coordinate with a code-reviewer agent to analyze three function specifications.

1. Start a code-reviewer agent with the full task embedded in its initial message:
   `scion start code-reviewer -y -t default --harness opencode --harness-auth none "Analyze these three function specifications and provide a detailed review:

   FUNCTION 1: calculateSum(numbers)
   - Description: Sums all numbers in a list
   - Input: array of integers
   - Output: integer sum
   - Edge cases: empty list returns 0, negative numbers allowed

   FUNCTION 2: findMax(numbers)
   - Description: Finds the maximum value in a list
   - Input: array of integers
   - Output: maximum integer
   - Edge cases: empty list returns null, single element returns itself

   FUNCTION 3: reverseString(s)
   - Description: Reverses a string
   - Input: string
   - Output: reversed string
   - Edge cases: empty string returns empty, single char returns itself

   Provide:
   (1) Time and space complexity for each (O notation)
   (2) Whether each edge case is handled correctly and if any are missing
   (3) One actionable improvement suggestion per function

   After completing your analysis, send your report back to me via:
   scion message orchestrator \"YOUR COMPLETE REPORT HERE WITH ALL THREE SECTIONS\""`

2. Set blocked status: `sciontool status blocked "Waiting for code-reviewer analysis"`
3. Wait for code-reviewer's message (harness will notify you)
4. Once you receive the report, create `/workspace/review-summary.md` containing:
   - A header section
   - code-reviewer's full report (copy their findings)
   - Your own brief assessment section at the end evaluating whether the functions are production-ready
5. Mark complete: `sciontool status task_completed "Multi-agent code review complete"`
```

**Start command**:
```bash
TASK=$(echo '...' | sed 's/"/\\"/g')
scion start orchestrator -y -t default --harness opencode --harness-auth none "$TASK"
```

**Monitor and validate**:
- Same checks as Approach 1, but code-reviewer should begin analysis immediately upon boot (no post-start message required)
- Verify the task content is preserved intact when embedded in the `scion start` message (no truncation or mangling)

---

### Code Review Test — Approach 3: (To Be Determined)

[Add approach description and task when designing next variant]

**Monitor and validate**:
[Define expected behavior to check]
