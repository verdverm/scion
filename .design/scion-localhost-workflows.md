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
scion look <name> --full --plain          # View terminal output (--full = complete scrollback, --plain = strip ANSI)
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
scion look <name> --full --plain
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

3. **Verify with Scion** — `scion list`
   - Scion is the source of truth, not container logs

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

