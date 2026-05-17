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

Buildx is configured via `buildkitd.toml` in the repo.

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

### Scion Server (Hub + Broker)

```bash
nohup ./scripts/local-scion-server.sh > /tmp/scion-server.log 2>&1 &
```

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

1. **Start server in background** — `nohup ./scripts/local-scion-server.sh > /tmp/scion-server.log 2>&1 &`
   - Logs to file so the command returns immediately
   - Don't use `pkill -f "scion server"` — it kills your own background process
   - Find PID with: `ps aux | grep "scion server" | grep -v grep | awk '{print $2}'`

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
